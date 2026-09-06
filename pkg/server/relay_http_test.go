package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestRelayEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, rawURL, rangeHeader string
		extensionless             bool
		want                      bool
	}{
		{"TS query", "http://provider/live/1.ts?token=a", "", false, true},
		{"TS case", "http://provider/live/1.TS", "", false, true},
		{"byte zero", "http://provider/live/1.ts", "bytes=0-", false, true},
		{"seek", "http://provider/live/1.ts", "bytes=188-", false, false},
		{"bounded range", "http://provider/live/1.ts", "bytes=0-100", false, false},
		{"multiple ranges", "http://provider/live/1.ts", "bytes=0-,100-", false, false},
		{"HLS", "http://provider/live/1.m3u8?token=a", "", true, false},
		{"VOD extension", "http://provider/live/1.mp4", "", true, false},
		{"Xtream movie in M3U", "http://provider/movie/user/pass/1.ts", "", false, false},
		{"Xtream series in M3U", "http://provider/series/user/pass/1.ts", "", false, false},
		{"Xtream timeshift in M3U", "http://provider/timeshift/user/pass/60/start/1.ts", "", false, false},
		{"M3U extensionless", "http://provider/live/1", "", false, false},
		{"Xtream extensionless", "http://provider/live/1", "", true, true},
		{"non HTTP", "file:///live/1.ts", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.rangeHeader != "" {
				req.Header.Set("Range", tc.rangeHeader)
			}
			upstream, _ := url.Parse(tc.rawURL)
			if got := relayEligible(req, upstream, tc.extensionless); got != tc.want {
				t.Fatalf("eligible=%v, want=%v", got, tc.want)
			}
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header["Range"] = []string{"bytes=0-", "bytes=0-"}
	upstream, _ := url.Parse("http://provider/1.ts")
	if relayEligible(req, upstream, false) {
		t.Fatal("duplicate ranges allowed")
	}
}

func relayHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(handler)
	s.Config.ConnContext = relayConnContext
	s.Start()
	t.Cleanup(s.Close)
	return s
}

func readHTTPPackets(t *testing.T, resp *http.Response, count int) []byte {
	t.Helper()
	data := make([]byte, count*relayPacketSize)
	read := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(resp.Body, data)
		read <- err
	}()
	select {
	case err := <-read:
		if err != nil {
			t.Fatalf("read live response: %v", err)
		}
	case <-time.After(3 * time.Second):
		resp.Body.Close()
		<-read
		t.Fatal("HTTP body stalled")
	}
	return data
}

func TestRelayHTTPFanoutHeadersStatusAndShutdown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var upstreamCalls int32
	receivedHeaders := make(chan http.Header, 5)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		receivedHeaders <- r.Header.Clone()
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Etag", "not-a-live-validator")
		w.Header().Set("Content-Range", "bytes 0-100/100")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Connection", "X-Private")
		w.Header().Set("X-Private", "hop")
		w.Header().Set("Set-Cookie", "upstream=private")
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := w.Write(tsPackets(1, 2)); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}))
	t.Cleanup(upstream.Close)
	m := testRelayManager(t, testRelayConfig())
	c := &Config{
		ProxyConfig: &config.ProxyConfig{
			User: "viewer", Password: "secret",
			XtreamUser: "up", XtreamPassword: "secret", XtreamBaseURL: upstream.URL,
		},
		relay: m,
	}
	router := gin.New()
	c.xtreamRoutes(router.Group("/"))
	router.GET("/status", c.status)
	proxy := relayHTTPServer(t, router)
	t.Cleanup(m.Close)
	newViewer := func(ua, auth string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, proxy.URL+"/live/viewer/secret/1.ts", nil)
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", ua)
		req.Header.Set("Authorization", auth)
		req.Header.Set("Cookie", "provider=session")
		req.Header.Set("Range", "bytes=0-")
		req.Header.Set("If-Range", "old")
		resp, err := proxy.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/mp2t" {
			t.Fatalf("unexpected response: %d %v", resp.StatusCode, resp.Header)
		}
		for _, key := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Etag", "X-Private", "Set-Cookie"} {
			if resp.Header.Get(key) != "" {
				t.Fatalf("relay forwarded %s", key)
			}
		}
		return resp
	}
	first := newViewer("player-one", "Bearer first")
	second := newViewer("player-two", "Bearer first")
	readHTTPPackets(t, first, 2)
	readHTTPPackets(t, second, 2)
	if atomic.LoadInt32(&upstreamCalls) != 1 {
		t.Fatal("HTTP viewers did not share one upstream")
	}
	headers := <-receivedHeaders
	if headers.Get("Range") != "" || headers.Get("If-Range") != "" ||
		headers.Get("Authorization") != "Bearer first" || headers.Get("Cookie") != "provider=session" ||
		headers.Get("User-Agent") != "player-one" {
		t.Fatalf("wrong upstream headers: %v", headers)
	}
	statusResp, err := proxy.Client().Get(proxy.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var status statusResponse
	err = json.NewDecoder(statusResp.Body).Decode(&status)
	statusResp.Body.Close()
	if err != nil || status.ActiveConnections != 2 || len(status.Connections) != 2 ||
		status.Relay == nil || status.Relay.Sessions != 1 || status.Relay.Upstreams != 1 || status.Relay.Viewers != 2 {
		t.Fatalf("status did not distinguish viewers/upstreams: %+v, %v", status, err)
	}
	for _, conn := range status.Connections {
		if !strings.HasPrefix(conn.URL, "relay:") || strings.Contains(conn.URL, "secret") {
			t.Fatal("status exposed upstream credentials")
		}
	}
	first.Body.Close()
	eventually(t, func() bool { return m.stats().Viewers == 1 })
	readHTTPPackets(t, second, 2)
	third := newViewer("player-three", "Bearer different")
	readHTTPPackets(t, third, 2)
	if atomic.LoadInt32(&upstreamCalls) != 2 {
		t.Fatal("different HTTP credentials were not isolated")
	}
	m.Close()
	eventually(t, func() bool { return len(activeTracker.active()) == 0 })
}

func TestRelayRoutesLeaveHLSVODAndRangesDirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls int32
	body := tsPackets(7, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body)
	}))
	t.Cleanup(upstream.Close)
	m := testRelayManager(t, testRelayConfig())
	c := &Config{
		ProxyConfig: &config.ProxyConfig{
			User: "viewer", Password: "secret", XtreamUser: "up", XtreamPassword: "secret",
			XtreamBaseURL: upstream.URL,
		},
		relay: m,
		playlist: &m3u.Playlist{Tracks: []m3u.Track{
			{URI: upstream.URL + "/live.ts", Length: -1},
			{URI: upstream.URL + "/vod.ts", Length: 100},
			{URI: upstream.URL + "/live.m3u8?token=test", Length: -1},
			{URI: upstream.URL + "/movie/up/secret/1.ts", Length: -1},
			{URI: upstream.URL + "/series/up/secret/1.ts", Length: -1},
			{URI: upstream.URL + "/timeshift/up/secret/60/start/1.ts", Length: -1},
		}},
		endpointAntiColision: "tracks",
	}
	router := gin.New()
	c.xtreamRoutes(router.Group("/"))
	c.m3uRoutes(router.Group("/"))
	for _, tc := range []struct{ path, rangeHeader string }{
		{"/movie/viewer/secret/1.ts", ""},
		{"/series/viewer/secret/1.ts", ""},
		{"/timeshift/viewer/secret/1/date/1.ts", ""},
		{"/live/viewer/secret/1.ts", "bytes=188-"},
		{"/tracks/viewer/secret/0/live.ts", "bytes=0-100"},
		{"/tracks/viewer/secret/1/vod.ts", ""},
		{"/tracks/viewer/secret/2/chunk.ts", ""},
		{"/tracks/viewer/secret/2/live.m3u8", ""},
		{"/tracks/viewer/secret/3/1.ts", ""},
		{"/tracks/viewer/secret/4/1.ts", ""},
		{"/tracks/viewer/secret/5/1.ts", ""},
	} {
		t.Run(tc.path+tc.rangeHeader, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.rangeHeader != "" {
				req.Header.Set("Range", tc.rangeHeader)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), body) ||
				w.Header().Get("Content-Length") == "" {
				t.Fatalf("direct route changed: status=%d headers=%v", w.Code, w.Header())
			}
			if m.stats().Sessions != 0 {
				t.Fatal("direct route started a relay")
			}
		})
	}
	if atomic.LoadInt32(&calls) != 11 {
		t.Fatalf("expected eleven direct upstreams, got %d", atomic.LoadInt32(&calls))
	}
}

func TestRelayM3UTrackSharesManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newRelayFixture(t)
	m := testRelayManager(t, testRelayConfig())
	c := &Config{
		ProxyConfig: &config.ProxyConfig{User: "viewer", Password: "secret"},
		relay:       m,
		playlist: &m3u.Playlist{Tracks: []m3u.Track{
			{URI: f.server.URL + "/live.ts?token=secret", Length: -1},
		}},
		endpointAntiColision: "tracks",
	}
	router := gin.New()
	c.m3uRoutes(router.Group("/"))
	proxy := relayHTTPServer(t, router)
	t.Cleanup(m.Close)
	first, err := proxy.Client().Get(proxy.URL + "/tracks/viewer/secret/0/live.ts")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Body.Close() })
	feed := openedFeed(t, f)
	secondDone := make(chan *http.Response, 1)
	go func() {
		resp, _ := proxy.Client().Get(proxy.URL + "/tracks/viewer/secret/0/live.ts")
		secondDone <- resp
	}()
	eventually(t, func() bool { return m.stats().Viewers == 2 })
	sendFeed(t, feed, tsPackets(3, 2))
	second := <-secondDone
	if second == nil {
		t.Fatal("second M3U viewer failed")
	}
	t.Cleanup(func() { second.Body.Close() })
	if got := readHTTPPackets(t, second, 2); !bytes.Equal(got, tsPackets(3, 2)) {
		t.Fatal("late M3U viewer did not start at live edge")
	}
	if atomic.LoadInt32(&f.calls) != 1 {
		t.Fatal("per-track handler created independent managers")
	}
}

func TestRelayHTTPBurstFanout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newRelayFixture(t)
	m := testRelayManager(t, testRelayConfig())
	upstream, _ := url.Parse(f.server.URL + "/live.ts")
	router := gin.New()
	router.GET("/live", func(ctx *gin.Context) { (&Config{relay: m}).relayStream(ctx, upstream) })
	proxy := relayHTTPServer(t, router)
	t.Cleanup(m.Close)
	first, err := proxy.Client().Get(proxy.URL + "/live")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Body.Close() })
	readHTTPPackets(t, first, 2)
	feed := openedFeed(t, f)
	secondReady := make(chan *http.Response, 1)
	go func() {
		resp, _ := proxy.Client().Get(proxy.URL + "/live")
		secondReady <- resp
	}()
	eventually(t, func() bool { return m.stats().Viewers == 2 })
	sendFeed(t, feed, tsPackets(1, 2))
	second := <-secondReady
	if second == nil {
		t.Fatal("second viewer failed to join")
	}
	t.Cleanup(func() { second.Body.Close() })
	readHTTPPackets(t, first, 2)
	readHTTPPackets(t, second, 2)

	// A provider may burst a GOP-sized block even at a modest average bitrate.
	// Both readers are healthy; transient scheduling must not look like a
	// persistently slow viewer.
	burst := tsPackets(2, 4096)
	for batch := 0; batch < 3; batch++ {
		readersReady := make(chan struct{}, 2)
		results := make(chan error, 2)
		for _, resp := range []*http.Response{first, second} {
			go func(resp *http.Response) {
				data := make([]byte, len(burst))
				readersReady <- struct{}{}
				_, err := io.ReadFull(resp.Body, data)
				if err == nil && !bytes.Equal(data, burst) {
					err = fmt.Errorf("live burst bytes changed")
				}
				results <- err
			}(resp)
		}
		<-readersReady
		<-readersReady
		sendFeed(t, feed, burst)
		for i := 0; i < 2; i++ {
			select {
			case err := <-results:
				if err != nil {
					t.Fatalf("healthy viewer lost during provider burst: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("provider burst did not reach both viewers")
			}
		}
	}
	if m.stats().SlowDisconnects != 0 || atomic.LoadInt32(&f.calls) != 1 {
		t.Fatalf("provider bursts should retain one shared upstream: %+v", m.stats())
	}
}

// A deadline-controlled writer models a downstream socket that stops accepting
// writes. net.Pipe exercises the same Go 1.17 deadline mechanism as the server.
type blockedRelayWriter struct {
	header  http.Header
	conn    net.Conn
	started chan struct{}
	once    sync.Once
}

func (w *blockedRelayWriter) Header() http.Header { return w.header }
func (w *blockedRelayWriter) WriteHeader(int)     {}
func (w *blockedRelayWriter) Flush()              {}
func (w *blockedRelayWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.conn.Write(b)
}

func TestRelayBlockedWriterShutdown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newRelayFixture(t)
	m := testRelayManager(t, testRelayConfig())
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	writer := &blockedRelayWriter{header: make(http.Header), conn: local, started: make(chan struct{})}
	ctx, _ := gin.CreateTestContext(writer)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx.Request = req.WithContext(relayConnContext(context.Background(), local))
	c := &Config{relay: m}
	upstream, _ := url.Parse(f.server.URL + "/live.ts")
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.relayStream(ctx, upstream)
	}()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("HTTP writer never started")
	}
	m.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown left a blocked HTTP writer hanging")
	}
}

func TestRelayBlockedWriterDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newRelayFixture(t)
	cfg := testRelayConfig()
	cfg.ReadTimeout = time.Minute
	cfg.IdleTimeout = 0
	m := testRelayManager(t, cfg)
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	writer := &blockedRelayWriter{header: make(http.Header), conn: local, started: make(chan struct{})}
	ctx, _ := gin.CreateTestContext(writer)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx.Request = req.WithContext(relayConnContext(context.Background(), local))
	upstream, _ := url.Parse(f.server.URL + "/live.ts")
	done := make(chan struct{})
	go func() {
		defer close(done)
		(&Config{relay: m}).relayStream(ctx, upstream)
	}()
	select {
	case <-done:
	case <-time.After(relayWriteTimeout + 2*time.Second):
		t.Fatal("write deadline did not release the hung downstream")
	}
	eventually(t, func() bool { return m.stats().Sessions == 0 })
}

func TestRelayLateViewerHasStartupDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newRelayFixture(t)
	m := testRelayManager(t, testRelayConfig())
	m.startup = 50 * time.Millisecond
	upstream, _ := url.Parse(f.server.URL + "/live.ts")
	first := subscribeRelay(t, m, upstream.String(), nil)
	receiveChunk(t, first)
	c := &Config{relay: m}
	router := gin.New()
	router.GET("/live", func(ctx *gin.Context) { c.relayStream(ctx, upstream) })
	proxy := relayHTTPServer(t, router)
	t.Cleanup(m.Close)
	response, err := proxy.Client().Get(proxy.URL + "/live")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("late stalled viewer status = %d, want 504", response.StatusCode)
	}
	if m.stats().Viewers != 1 {
		t.Fatal("late viewer timeout affected the existing viewer")
	}
}

func TestRelayResponseNormalization(t *testing.T) {
	h := relayResponseHeaders(http.Header{
		"Content-Length": {"100"}, "Content-Range": {"bytes 0-99/100"}, "Accept-Ranges": {"bytes"},
		"Etag": {"old"}, "Last-Modified": {"old"}, "Content-Type": {"video/mp2t"},
	})
	if h.Get("Content-Type") != "video/mp2t" || h.Get("Cache-Control") != "no-store" {
		t.Fatal("live response metadata missing")
	}

	if len(h) != 2 {
		t.Fatalf("stale finite response metadata: %v", h)
	}
}

func TestRelayServerDefaultsOptOutAndValidation(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		cfg := config.DefaultRelayConfig()
		cfg.Enabled = enabled
		proxyConfig := &config.ProxyConfig{Relay: &cfg}
		if enabled {
			proxyConfig.Relay = nil // Programmatic callers also get defaults.
		}
		c, err := NewServer(proxyConfig)
		if err != nil {
			t.Fatal(err)
		}
		if c.relay != nil {
			t.Cleanup(c.relay.Close)
		}
		if (c.relay != nil) != enabled {
			t.Fatalf("enabled=%v, manager=%v", enabled, c.relay)
		}
	}
	cfg := config.DefaultRelayConfig()
	cfg.ReadTimeout = -time.Second
	if _, err := NewServer(&config.ProxyConfig{Relay: &cfg}); err == nil {
		t.Fatal("server accepted invalid relay config")
	}
}
