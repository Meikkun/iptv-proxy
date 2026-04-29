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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/utils"
	xtreamapi "github.com/pierre-emmanuelJ/iptv-proxy/pkg/xtream-proxy"
	uuid "github.com/satori/go.uuid"
	xtream "github.com/tellytv/go.xtream-codes"
)

type cacheMeta struct {
	path      string
	createdAt time.Time
}

type hlsRedirectMeta struct {
	targetURL url.URL
	createdAt time.Time
}

const hlsRedirectTTL = 10 * time.Minute

var hlsChannelsRedirectURL map[string]hlsRedirectMeta = map[string]hlsRedirectMeta{}
var hlsChannelsRedirectURLLock = sync.RWMutex{}

// XXX Use key/value storage e.g: etcd, redis...
// and remove that dirty globals
var xtreamM3uCache map[string]cacheMeta = map[string]cacheMeta{}
var xtreamM3uCacheLock = sync.RWMutex{}

func (c *Config) cacheXtreamM3u(playlist *m3u.Playlist, cacheName string) error {
	xtreamM3uCacheLock.Lock()
	defer xtreamM3uCacheLock.Unlock()

	tmp := *c
	tmp.playlist = playlist

	playlistPath := filepath.Join(os.TempDir(), uuid.NewV4().String()+".iptv-proxy.m3u")
	f, err := os.Create(playlistPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := tmp.marshallInto(f, true); err != nil {
		return err
	}

	if existing, ok := xtreamM3uCache[cacheName]; ok && existing.path != "" && existing.path != playlistPath {
		if removeErr := os.Remove(existing.path); removeErr != nil && !os.IsNotExist(removeErr) {
			utils.DebugLog("unable to remove stale xtream cache file %q: %v", existing.path, removeErr)
		}
	}

	xtreamM3uCache[cacheName] = cacheMeta{
		path:      playlistPath,
		createdAt: time.Now(),
	}

	return nil
}

func xtreamCacheTTL(expirationHours int) time.Duration {
	if expirationHours <= 0 {
		return 0
	}

	return time.Duration(expirationHours) * time.Hour
}

func isXtreamCacheExpired(meta cacheMeta, now time.Time, ttl time.Duration) bool {
	if meta.path == "" || meta.createdAt.IsZero() {
		return true
	}

	if ttl <= 0 {
		return true
	}

	return now.Sub(meta.createdAt) >= ttl
}

func cleanupExpiredXtreamCacheEntries(now time.Time, ttl time.Duration) {
	for cacheKey, meta := range xtreamM3uCache {
		if !isXtreamCacheExpired(meta, now, ttl) {
			continue
		}

		if meta.path != "" {
			if err := os.Remove(meta.path); err != nil && !os.IsNotExist(err) {
				utils.DebugLog("unable to remove expired xtream cache file %q: %v", meta.path, err)
			}
		}

		delete(xtreamM3uCache, cacheKey)
	}
}

func (c *Config) xtreamGenerateM3u(ctx *gin.Context, extension string) (*m3u.Playlist, error) {
	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())
	if err != nil {
		return nil, utils.PrintErrorAndReturn(err)
	}

	cat, err := client.GetLiveCategories()
	if err != nil {
		return nil, utils.PrintErrorAndReturn(err)
	}

	// this is specific to xtream API,
	// prefix with "live" if there is an extension.
	var prefix string
	if extension != "" {
		extension = "." + extension
		prefix = "live/"
	}

	var playlist = new(m3u.Playlist)
	playlist.Tracks = make([]m3u.Track, 0)

	for _, category := range cat {
		live, err := client.GetLiveStreams(fmt.Sprint(category.ID))
		if err != nil {
			return nil, utils.PrintErrorAndReturn(err)
		}

		for _, stream := range live {
			track := m3u.Track{Name: stream.Name, Length: -1, URI: "", Tags: nil}

			//TODO: Add more tag if needed.
			if stream.EPGChannelID != "" {
				track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-id", Value: stream.EPGChannelID})
			}
			if stream.Name != "" {
				track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-name", Value: stream.Name})
			}
			if stream.Icon != "" {
				track.Tags = append(track.Tags, m3u.Tag{Name: "tvg-logo", Value: stream.Icon})
			}
			if category.Name != "" {
				track.Tags = append(track.Tags, m3u.Tag{Name: "group-title", Value: category.Name})
			}

			track.URI = fmt.Sprintf("%s/%s%s/%s/%s%s", c.XtreamBaseURL, prefix, c.XtreamUser, c.XtreamPassword, fmt.Sprint(stream.ID), extension)
			playlist.Tracks = append(playlist.Tracks, track)
		}
	}

	return playlist, nil
}

func (c *Config) xtreamGetAuto(ctx *gin.Context) {
	newQuery := ctx.Request.URL.Query()
	q := c.RemoteURL.Query()
	for k, v := range q {
		if k == "username" || k == "password" {
			continue
		}

		newQuery.Add(k, strings.Join(v, ","))
	}
	ctx.Request.URL.RawQuery = newQuery.Encode()

	c.xtreamGet(ctx)
}

func (c *Config) xtreamGet(ctx *gin.Context) {
	rawURL := fmt.Sprintf("%s/get.php?username=%s&password=%s", c.XtreamBaseURL, c.XtreamUser, c.XtreamPassword)

	q := ctx.Request.URL.Query()

	for k, v := range q {
		if k == "username" || k == "password" {
			continue
		}

		rawURL = fmt.Sprintf("%s&%s=%s", rawURL, k, strings.Join(v, ","))
	}

	m3uURL, err := url.Parse(rawURL)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	ttl := xtreamCacheTTL(c.M3UCacheExpiration)
	cacheKey := m3uURL.String()
	now := time.Now()

	xtreamM3uCacheLock.Lock()
	cleanupExpiredXtreamCacheEntries(now, ttl)
	meta, ok := xtreamM3uCache[cacheKey]
	xtreamM3uCacheLock.Unlock()

	if !ok || isXtreamCacheExpired(meta, now, ttl) {
		log.Printf("[iptv-proxy] %v | %s | xtream cache m3u file\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		playlist, err := m3u.Parse(m3uURL.String())
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
		if err := c.cacheXtreamM3u(&playlist, cacheKey); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	cachePath := xtreamM3uCache[cacheKey].path
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(cachePath)
}

func (c *Config) xtreamApiGet(ctx *gin.Context) {
	const (
		apiGet = "apiget"
	)

	var (
		extension = ctx.Query("output")
		cacheName = apiGet + extension
	)

	ttl := xtreamCacheTTL(c.M3UCacheExpiration)
	now := time.Now()

	xtreamM3uCacheLock.Lock()
	cleanupExpiredXtreamCacheEntries(now, ttl)
	meta, ok := xtreamM3uCache[cacheName]
	xtreamM3uCacheLock.Unlock()

	if !ok || isXtreamCacheExpired(meta, now, ttl) {
		log.Printf("[iptv-proxy] %v | %s | xtream cache API m3u file\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		playlist, err := c.xtreamGenerateM3u(ctx, extension)
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
		if err := c.cacheXtreamM3u(playlist, cacheName); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	cachePath := xtreamM3uCache[cacheName].path
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(cachePath)

}

func (c *Config) xtreamPlayerAPIGET(ctx *gin.Context) {
	c.xtreamPlayerAPI(ctx, ctx.Request.URL.Query())
}

func (c *Config) xtreamPlayerAPIPOST(ctx *gin.Context) {
	contents, err := readLimitedRequestBody(ctx.Request.Body, maxFormBodyBytes)
	if err != nil {
		if errors.Is(err, errRequestBodyTooLarge) {
			ctx.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	q, err := url.ParseQuery(string(contents))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	c.xtreamPlayerAPI(ctx, q)
}

func (c *Config) xtreamPlayerAPI(ctx *gin.Context, q url.Values) {
	var action string
	if len(q["action"]) > 0 {
		action = q["action"][0]
	}

	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	resp, httpcode, contentType, err := client.Action(c.ProxyConfig, action, q)
	if err != nil {
		httpcode = normalizeHTTPStatusForError(httpcode, err)
		ctx.AbortWithError(httpcode, utils.PrintErrorAndReturn(err))
		return
	}

	log.Printf("[iptv-proxy] %v | %s |Action\t%s\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP(), action)

	processedResp := ProcessResponse(resp)

	if config.CacheFolder != "" {
		readableJSON, _ := json.Marshal(processedResp)
		utils.WriteResponseToFile(ctx, readableJSON, contentType)
	}

	ctx.JSON(http.StatusOK, processedResp)
}

// ProcessResponse processes various types of xtream-codes responses
func ProcessResponse(resp interface{}) interface{} {

	respType := reflect.TypeOf(resp)

	switch {
	case respType == nil:
		return resp
	case strings.Contains(respType.String(), "[]xtreamcodes."):
		return processXtreamArray(resp)
	case strings.Contains(respType.String(), "xtreamcodes."):
		return processXtreamStruct(resp)
	default:
	}
	return resp
}

func processXtreamArray(arr interface{}) interface{} {
	v := reflect.ValueOf(arr)
	if v.Kind() != reflect.Slice {
		return arr
	}

	if v.Len() == 0 {
		return arr
	}

	// Check if the first item is an xtreamcodes struct having a Fields field
	if !isXtreamCodesStruct(v.Index(0).Interface()) {
		return arr
	}

	result := make([]interface{}, v.Len())
	for i := 0; i < v.Len(); i++ {
		result[i] = processXtreamStruct(v.Index(i).Interface())
	}

	return result
}

// Define a helper function to check if fields exist
func hasFieldsField(item interface{}) bool {
	respValue := reflect.ValueOf(item)
	if respValue.Kind() == reflect.Ptr {
		respValue = respValue.Elem()
	}

	// Check for specific fields, e.g., "Fields"
	fieldValue := respValue.FieldByName(xtream.StructFields)
	return fieldValue.IsValid() && !fieldValue.IsNil()
}

func isXtreamCodesStruct(item interface{}) bool {
	respType := reflect.TypeOf(item)
	return respType != nil && strings.Contains(respType.String(), "xtreamcodes.") && hasFieldsField(item)
}

func processXtreamStruct(item interface{}) interface{} {
	if isXtreamCodesStruct(item) {
		respValue := reflect.ValueOf(item)
		if respValue.Kind() == reflect.Ptr {
			respValue = respValue.Elem()
		}

		fieldValue := respValue.FieldByName(xtream.StructFields)
		if fieldValue.IsValid() && !fieldValue.IsNil() {

			if fieldValue.Kind() == reflect.Slice && fieldValue.Type().Elem().Kind() == reflect.Uint8 {
				var unmarshaledValue interface{}
				err := json.Unmarshal(fieldValue.Interface().([]byte), &unmarshaledValue)
				if err != nil {
					utils.DebugLog("-- processXtreamStruct: JSON unmarshal error: %v", err)
					return fieldValue.Interface()
				}
				return unmarshaledValue
			}

			return fieldValue.Interface()
		}
	}
	return item
}

func (c *Config) xtreamXMLTV(ctx *gin.Context) {
	client, err := xtreamapi.New(c.XtreamUser.String(), c.XtreamPassword.String(), c.XtreamBaseURL, ctx.Request.UserAgent())
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	resp, err := client.GetXMLTV()
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	// Write response to file
	// utils.WriteResponseToFile(ctx, resp)

	ctx.Data(http.StatusOK, "application/xml", resp)
}

func (c *Config) xtreamStreamHandler(ctx *gin.Context) {
	id := ctx.Param("id")
	rpURL, err := url.Parse(fmt.Sprintf("%s/%s/%s/%s", c.XtreamBaseURL, c.XtreamUser, c.XtreamPassword, id))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamStreamLive(ctx *gin.Context) {
	id := ctx.Param("id")
	rpURL, err := url.Parse(fmt.Sprintf("%s/live/%s/%s/%s", c.XtreamBaseURL, c.XtreamUser, c.XtreamPassword, id))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamStreamPlay(ctx *gin.Context) {
	token := ctx.Param("token")
	t := ctx.Param("type")
	rpURL, err := url.Parse(fmt.Sprintf("%s/play/%s/%s", c.XtreamBaseURL, token, t))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamStreamTimeshift(ctx *gin.Context) {
	duration := ctx.Param("duration")
	start := ctx.Param("start")
	id := ctx.Param("id")
	rpURL, err := url.Parse(fmt.Sprintf("%s/timeshift/%s/%s/%s/%s/%s", c.XtreamBaseURL, c.XtreamUser, c.XtreamPassword, duration, start, id))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	c.stream(ctx, rpURL)
}

func (c *Config) xtreamStreamMovie(ctx *gin.Context) {
	id := ctx.Param("id")
	rpURL, err := url.Parse(fmt.Sprintf("%s/movie/%s/%s/%s", c.XtreamBaseURL, c.XtreamUser, c.XtreamPassword, id))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamStreamSeries(ctx *gin.Context) {
	id := ctx.Param("id")
	rpURL, err := url.Parse(fmt.Sprintf("%s/series/%s/%s/%s", c.XtreamBaseURL, c.XtreamUser, c.XtreamPassword, id))
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, rpURL)
}

func (c *Config) xtreamHlsStream(ctx *gin.Context) {
	chunk := ctx.Param("chunk")
	s := strings.Split(chunk, "_")
	if len(s) != 2 {
		ctx.AbortWithError( // nolint: errcheck
			http.StatusInternalServerError,
			utils.PrintErrorAndReturn(errors.New("HSL malformed chunk")),
		)
		return
	}
	channel := s[0]

	redirectURL, err := getHlsRedirectURL(c, channel)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	req, err := url.Parse(
		fmt.Sprintf(
			"%s://%s/hls/%s/%s",
			redirectURL.Scheme,
			redirectURL.Host,
			ctx.Param("token"),
			ctx.Param("chunk"),
		),
	)

	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, req)
}

func (c *Config) xtreamHlsrStream(ctx *gin.Context) {
	channel := ctx.Param("channel")

	redirectURL, err := getHlsRedirectURL(c, channel)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	req, err := url.Parse(
		fmt.Sprintf(
			"%s://%s/hlsr/%s/%s/%s/%s/%s/%s",
			redirectURL.Scheme,
			redirectURL.Host,
			ctx.Param("token"),
			c.XtreamUser,
			c.XtreamPassword,
			ctx.Param("channel"),
			ctx.Param("hash"),
			ctx.Param("chunk"),
		),
	)

	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	c.xtreamStream(ctx, req)
}

func getHlsRedirectURL(c *Config, channel string) (*url.URL, error) {
	hlsChannelsRedirectURLLock.Lock()
	defer hlsChannelsRedirectURLLock.Unlock()
	pruneHLSRedirectsLocked(time.Now())

	meta, ok := hlsChannelsRedirectURL[hlsRedirectKey(c, channel)]
	if !ok {
		return nil, utils.PrintErrorAndReturn(errors.New("HSL redirect url not found"))
	}

	targetURL := meta.targetURL
	return &targetURL, nil
}

func (c *Config) hlsXtreamStream(ctx *gin.Context, oriURL *url.URL) {
	client := &http.Client{
		Timeout: defaultUpstreamRequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequest("GET", oriURL.String(), nil)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}

	mergeHttpHeader(req.Header, ctx.Request.Header)

	resp, err := client.Do(req)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusFound {
		location, err := resp.Location()
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
		id := ctx.Param("id")
		if strings.Contains(location.String(), id) {
			hlsChannelsRedirectURLLock.Lock()
			pruneHLSRedirectsLocked(time.Now())
			hlsChannelsRedirectURL[hlsRedirectKey(c, id)] = hlsRedirectMeta{
				targetURL: *location,
				createdAt: time.Now(),
			}
			hlsChannelsRedirectURLLock.Unlock()

			hlsReq, err := http.NewRequest("GET", location.String(), nil)
			if err != nil {
				ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
				return
			}

			mergeHttpHeader(hlsReq.Header, ctx.Request.Header)

			hlsResp, err := client.Do(hlsReq)
			if err != nil {
				ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
				return
			}
			defer hlsResp.Body.Close()

			b, err := io.ReadAll(hlsResp.Body)
			if err != nil {
				ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
				return
			}
			body := string(b)
			body = strings.ReplaceAll(body, "/"+c.XtreamUser.String()+"/"+c.XtreamPassword.String()+"/", "/"+c.User.String()+"/"+c.Password.String()+"/")

			mergeHttpHeader(ctx.Writer.Header(), hlsResp.Header)

			ctx.Data(http.StatusOK, hlsResp.Header.Get("Content-Type"), []byte(body))
			return
		}
		ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(errors.New("Unable to HLS stream"))) // nolint: errcheck
		return
	}

	ctx.Status(resp.StatusCode)
}

func normalizeHTTPStatusForError(statusCode int, err error) int {
	if err == nil {
		return statusCode
	}

	if statusCode == 0 {
		return http.StatusInternalServerError
	}

	return statusCode
}

func hlsRedirectKey(c *Config, channel string) string {
	canonicalChannel := strings.TrimSuffix(channel, ".m3u8")
	return fmt.Sprintf("%s|%s|%s|%s", c.XtreamBaseURL, c.XtreamUser, c.XtreamPassword, canonicalChannel)
}

func pruneHLSRedirectsLocked(now time.Time) {
	for key, meta := range hlsChannelsRedirectURL {
		if now.Sub(meta.createdAt) < hlsRedirectTTL {
			continue
		}

		delete(hlsChannelsRedirectURL, key)
	}
}
