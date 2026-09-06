/*
 * Iptv-Proxy is a project to proxyfie an m3u file and to proxyfie an Xtream iptv service (client API).
 * Copyright (C) 2020  Pierre-Emmanuel Jacquier
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package server

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
	uuid "github.com/satori/go.uuid"

	"github.com/gin-gonic/gin"
)

const shutdownTimeout = 15 * time.Second

// Config represent the server configuration
type Config struct {
	*config.ProxyConfig

	// M3U service part
	playlist *m3u.Playlist
	// unique group-title values discovered before filtering
	availableGroups []string
	// this variable is set only for m3u proxy endpoints
	track *m3u.Track
	// path to the proxyfied m3u file
	proxyfiedM3UPath string

	endpointAntiColision string
	relay                *relayManager
	catalogue            *catalogueManager
}

// NewServer initialize a new server configuration
func NewServer(config *config.ProxyConfig) (*Config, error) {
	return NewServerContext(context.Background(), config)
}

// NewServerContext allows shutdown to interrupt playlist bootstrap.
func NewServerContext(ctx context.Context, config *config.ProxyConfig) (*Config, error) {
	relayConfig := relayConfiguration(config)
	if err := relayConfig.Validate(); err != nil {
		return nil, err
	}
	playlistConfig := playlistConfiguration(config)
	if err := playlistConfig.Validate(config.M3USources); err != nil {
		return nil, err
	}

	endpointAntiColision := strings.Split(uuid.NewV4().String(), "-")[0]
	if trimmedCustomId := strings.Trim(config.CustomId, "/"); trimmedCustomId != "" {
		endpointAntiColision = trimmedCustomId
	}

	server := &Config{
		ProxyConfig:          config,
		track:                nil,
		endpointAntiColision: endpointAntiColision,
		catalogue:            newCatalogueManager(playlistConfig, len(config.M3USources)),
	}
	if err := server.bootstrapCatalogue(ctx); err != nil {
		return nil, err
	}
	if relayConfig.Enabled {
		server.relay = newRelayManager(relayConfig, streamingHTTPClient)
	}
	return server, nil
}

// Serve the iptv-proxy api
func (c *Config) Serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return c.ServeContext(ctx)
}

func (c *Config) ServeContext(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if c.relay != nil {
		defer c.relay.Close()
	}
	if err := c.playlistInitialization(); err != nil {
		return err
	}

	router := c.router()
	if c.catalogue != nil {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-hup:
					c.RequestCatalogueRefresh()
				}
			}
		}()
		refreshDone := make(chan struct{})
		go func() { defer close(refreshDone); c.runCatalogueRefresh(ctx) }()
		defer func() { cancel(); <-refreshDone }()
	}

	srv := &http.Server{
		Addr:        fmt.Sprintf(":%d", c.HostConfig.Port),
		Handler:     router,
		ConnContext: relayConnContext,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("[iptv-proxy] Server is ready and listening on :%d", c.HostConfig.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		if c.relay != nil {
			c.relay.Close()
		}
		log.Printf("[iptv-proxy] Shutting down gracefully (timeout %s)...", shutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown error: %w", err)
		}
		log.Printf("[iptv-proxy] Server stopped")
		return nil
	}
}

// Groups returns the discovered M3U group-title values before filtering.
func (c *Config) Groups() []string {
	if c.catalogue != nil {
		return append([]string(nil), c.catalogue.snapshot().groups...)
	}
	groups := make([]string, len(c.availableGroups))
	copy(groups, c.availableGroups)

	return groups
}

func (c *Config) playlistInitialization() error {
	if c.catalogue != nil {
		return nil
	}
	if len(c.playlist.Tracks) == 0 {
		return nil
	}

	f, err := os.Create(c.proxyfiedM3UPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return c.marshallInto(f, false)
}

// MarshallInto a *bufio.Writer a Playlist.
func (c *Config) marshallInto(into *os.File, xtream bool) error {
	filteredTrack := make([]m3u.Track, 0, len(c.playlist.Tracks))

	ret := 0
	into.WriteString("#EXTM3U\n") // nolint: errcheck
	for i, track := range c.playlist.Tracks {
		var buffer bytes.Buffer

		buffer.WriteString("#EXTINF:")                       // nolint: errcheck
		buffer.WriteString(fmt.Sprintf("%d ", track.Length)) // nolint: errcheck
		for i := range track.Tags {
			if i == len(track.Tags)-1 {
				buffer.WriteString(fmt.Sprintf("%s=%q", track.Tags[i].Name, track.Tags[i].Value)) // nolint: errcheck
				continue
			}
			buffer.WriteString(fmt.Sprintf("%s=%q ", track.Tags[i].Name, track.Tags[i].Value)) // nolint: errcheck
		}

		uri, err := c.replaceURL(track.URI, i-ret, xtream)
		if err != nil {
			ret++
			log.Printf("ERROR: track: %s: %s", track.Name, err)
			continue
		}

		into.WriteString(fmt.Sprintf("%s, %s\n%s\n", buffer.String(), track.Name, uri)) // nolint: errcheck

		filteredTrack = append(filteredTrack, track)
	}
	c.playlist.Tracks = filteredTrack

	return into.Sync()
}

// ReplaceURL replace original playlist url by proxy url
func (c *Config) replaceURL(uri string, trackIndex int, xtream bool) (string, error) {
	return c.replaceURLToken(uri, strconv.Itoa(trackIndex), xtream)
}

func (c *Config) replaceURLToken(uri, token string, xtream bool) (string, error) {
	oriURL, err := url.Parse(uri)
	if err != nil {
		return "", err
	}

	protocol := "http"
	if c.HTTPS {
		protocol = "https"
	}

	customEnd := strings.Trim(c.CustomEndpoint, "/")
	if customEnd != "" {
		customEnd = fmt.Sprintf("/%s", customEnd)
	}

	uriPath := oriURL.EscapedPath()
	if xtream {
		uriPath = strings.ReplaceAll(uriPath, c.XtreamUser.PathEscape(), c.User.PathEscape())
		uriPath = strings.ReplaceAll(uriPath, c.XtreamPassword.PathEscape(), c.Password.PathEscape())
	} else {
		uriPath = path.Join("/", c.endpointAntiColision, c.User.PathEscape(), c.Password.PathEscape(), token, path.Base(uriPath))
	}

	basicAuth := oriURL.User.String()
	if !xtream {
		basicAuth = ""
	}
	if basicAuth != "" {
		basicAuth += "@"
	}

	authority := advertisedAuthority(protocol, c.HostConfig.Hostname, c.AdvertisedPort)
	newURI := fmt.Sprintf(
		"%s://%s%s%s%s",
		protocol,
		basicAuth,
		authority,
		customEnd,
		uriPath,
	)

	newURL, err := url.Parse(newURI)
	if err != nil {
		return "", err
	}

	return newURL.String(), nil
}

func (c *Config) router() *gin.Engine {
	router := gin.New()
	router.Use(requestLogger(), safeRecovery(), cors.Default())
	c.routes(router.Group("/"))
	return router
}

func advertisedAuthority(protocol, hostname string, port int) string {
	if shouldOmitAdvertisedPort(protocol, port) {
		return hostname
	}

	return net.JoinHostPort(hostname, strconv.Itoa(port))
}

func shouldOmitAdvertisedPort(protocol string, port int) bool {
	return (protocol == "https" && port == 443) || (protocol == "http" && port == 80)
}
