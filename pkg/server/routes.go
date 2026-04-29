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
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (c *Config) routes(r *gin.RouterGroup) {
	r = r.Group(c.CustomEndpoint)

	//Xtream service endopoints
	if c.ProxyConfig.XtreamBaseURL != "" {
		c.xtreamRoutes(r)
		if c.RemoteURL != nil &&
			c.RemoteURL.Host != "" &&
			strings.Contains(c.XtreamBaseURL, c.RemoteURL.Host) &&
			c.XtreamUser.String() == c.RemoteURL.Query().Get("username") &&
			c.XtreamPassword.String() == c.RemoteURL.Query().Get("password") {

			r.GET("/"+c.M3UFileName, c.authenticate, c.xtreamGetAuto)
			// XXX Private need: for external Android app
			r.POST("/"+c.M3UFileName, c.authenticate, c.xtreamGetAuto)

			return
		}
	}

	c.m3uRoutes(r)
}

func (c *Config) xtreamRoutes(r *gin.RouterGroup) {
	getphp := gin.HandlerFunc(c.xtreamGet)
	if c.XtreamGenerateApiGet {
		getphp = c.xtreamApiGet
	}
	r.GET("/get.php", c.authenticate, getphp)
	r.POST("/get.php", c.authenticate, getphp)
	r.GET("/apiget", c.authenticate, c.xtreamApiGet)
	r.GET("/player_api.php", c.authenticate, c.xtreamPlayerAPIGET)
	r.POST("/player_api.php", c.appAuthenticate, c.xtreamPlayerAPIPOST)
	r.GET("/xmltv.php", c.authenticate, c.xtreamXMLTV)
	r.GET(fmt.Sprintf("/%s/%s/:id", c.User, c.Password), c.xtreamStreamHandler)
	r.GET(fmt.Sprintf("/live/%s/%s/:id", c.User, c.Password), c.xtreamStreamLive)
	r.GET(fmt.Sprintf("/timeshift/%s/%s/:duration/:start/:id", c.User, c.Password), c.xtreamStreamTimeshift)
	r.GET(fmt.Sprintf("/movie/%s/%s/:id", c.User, c.Password), c.xtreamStreamMovie)
	r.GET(fmt.Sprintf("/series/%s/%s/:id", c.User, c.Password), c.xtreamStreamSeries)
	r.GET(fmt.Sprintf("/hlsr/:token/%s/%s/:channel/:hash/:chunk", c.User, c.Password), c.xtreamHlsrStream)
	r.GET("/hls/:token/:chunk", c.xtreamHlsStream)
	r.GET("/play/:token/:type", c.xtreamStreamPlay)
}

func (c *Config) m3uRoutes(r *gin.RouterGroup) {
	r.GET("/"+c.M3UFileName, c.authenticate, c.getM3U)
	// XXX Private need: for external Android app
	r.POST("/"+c.M3UFileName, c.authenticate, c.getM3U)
	r.GET(fmt.Sprintf("/%s/%s/%s/:track/*id", c.endpointAntiColision, c.User, c.Password), c.m3uTrackProxy)
}

func (c *Config) m3uTrackProxy(ctx *gin.Context) {
	trackIndex, err := strconv.Atoi(ctx.Param("track"))
	if err != nil || trackIndex < 0 || trackIndex >= len(c.playlist.Tracks) {
		ctx.AbortWithStatus(http.StatusNotFound)
		return
	}

	track := &c.playlist.Tracks[trackIndex]
	trackConfig := &Config{
		ProxyConfig:          c.ProxyConfig,
		relayManager:         c.relayManager,
		track:                track,
		endpointAntiColision: c.endpointAntiColision,
	}

	requestID := strings.TrimPrefix(ctx.Param("id"), "/")
	if requestID == "" {
		ctx.AbortWithStatus(http.StatusNotFound)
		return
	}

	if trackHasHLSExtension(track.URI) {
		trackConfig.m3u8ReverseProxy(ctx)
		return
	}

	expectedBase, err := trackPathBase(track.URI)
	if err != nil || requestID != expectedBase {
		ctx.AbortWithStatus(http.StatusNotFound)
		return
	}

	trackConfig.reverseProxy(ctx)
}

func trackHasHLSExtension(rawURI string) bool {
	parsedURI, err := url.Parse(rawURI)
	if err != nil {
		return false
	}

	return strings.EqualFold(path.Ext(parsedURI.Path), ".m3u8")
}

func trackPathBase(rawURI string) (string, error) {
	parsedURI, err := url.Parse(rawURI)
	if err != nil {
		return "", err
	}

	base := path.Base(parsedURI.Path)
	if base == "." || base == "/" || base == "" {
		return "", fmt.Errorf("missing path base in uri %q", rawURI)
	}

	return base, nil
}
