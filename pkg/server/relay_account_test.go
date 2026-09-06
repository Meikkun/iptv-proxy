package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestRelayOccupiedAccountServesExistingChannel(t *testing.T) {
	f := newRelayFixture(t)
	cfg := testRelayConfig()
	cfg.ExistingChannelWins = true
	m := testRelayManager(t, cfg)
	first := subscribeRelay(t, m, f.server.URL+"/live/user/pass/A.ts", nil)
	receiveChunk(t, first)
	feed := openedFeed(t, f)
	second := subscribeRelay(t, m, f.server.URL+"/live/user/pass/B.ts", nil)
	if first.session != second.session || atomic.LoadInt32(&f.calls) != 1 {
		t.Fatal("channel B opened a conflicting upstream instead of joining A")
	}
	sendFeed(t, feed, tsPackets(7, 2))
	if !bytes.Equal(receiveChunk(t, first), receiveChunk(t, second)) {
		t.Fatal("viewers received different live channels")
	}
	first.release()
	third := subscribeRelay(t, m, f.server.URL+"/live/user/pass/C.ts", nil)
	if third.session != second.session {
		t.Fatal("original viewer leaving abandoned the remaining viewer's channel")
	}
	second.release()
	third.release()
	fourth := subscribeRelay(t, m, f.server.URL+"/live/user/pass/B.ts", nil)
	if fourth.session == first.session || atomic.LoadInt32(&f.calls) != 2 {
		t.Fatal("unoccupied account did not switch from idle A to requested B")
	}
}

func TestRelayOccupiedAccountSimultaneousDifferentChannels(t *testing.T) {
	f := newRelayFixture(t)
	cfg := testRelayConfig()
	cfg.ExistingChannelWins = true
	m := testRelayManager(t, cfg)
	start := make(chan struct{})
	results := make(chan *relaySubscription, 2)
	for _, channel := range []string{"A.ts", "B.ts"} {
		go func(channel string) {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sub, _ := m.subscribe(ctx, f.server.URL+"/live/user/pass/"+channel, nil)
			results <- sub
		}(channel)
	}
	close(start)
	first, second := <-results, <-results
	if first == nil || second == nil {
		t.Fatal("concurrent channel selection failed")
	}
	defer first.release()
	defer second.release()
	if first.session != second.session || atomic.LoadInt32(&f.calls) != 1 {
		t.Fatal("simultaneous channel requests raced into two upstreams")
	}
}

func TestRelayOccupiedAccountIsolation(t *testing.T) {
	f := newRelayFixture(t)
	cfg := testRelayConfig()
	cfg.ExistingChannelWins = true
	m := testRelayManager(t, cfg)
	headers := http.Header{"Authorization": {"Bearer one"}}
	first := subscribeRelay(t, m, f.server.URL+"/live/user/pass/A.ts", headers)
	receiveChunk(t, first)
	other := subscribeRelay(t, m, f.server.URL+"/live/other/pass/B.ts", headers)
	if other.session == first.session {
		t.Fatal("different accounts were substituted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sub, code := m.subscribe(ctx, f.server.URL+"/live/user/pass/B.ts", http.Header{"Authorization": {"Bearer other"}})
	if sub != nil || code != http.StatusConflict {
		if sub != nil {
			sub.release()
		}
		t.Fatal("incompatible authorization must not share or open a conflicting upstream")
	}
	if atomic.LoadInt32(&f.calls) != 2 {
		t.Fatal("authorization conflict contacted the provider")
	}
}

func TestRelayAccountKey(t *testing.T) {
	base := relayAccountKey("http://provider/live/user/pass/A.ts")
	if base == "" {
		t.Fatal("Xtream live account not recognized")
	}
	for _, rawURL := range []string{
		"http://provider/live/user/pass/B.ts?token=channel-specific",
		"http://provider:80/user/pass/C.ts",
		"http://PROVIDER/live/%75ser/pass/A.ts",
		"http://provider/movie/user/pass/1.ts",
		"http://provider/series/user/pass/1.ts",
		"http://provider/timeshift/user/pass/60/date/1.ts",
	} {
		if got := relayAccountKey(rawURL); got != base {
			t.Errorf("same account split: %s", rawURL)
		}
	}
	for _, rawURL := range []string{
		"http://other/live/user/pass/A.ts",
		"https://provider/live/user/pass/A.ts",
		"http://provider:8080/live/user/pass/A.ts",
		"http://provider/live/other/pass/A.ts",
		"http://provider/live/user/other/A.ts",
	} {
		if got := relayAccountKey(rawURL); got == "" || got == base {
			t.Errorf("different account not isolated: %s", rawURL)
		}
	}
	for _, rawURL := range []string{
		"http://provider/channel.ts", "http://provider/live//pass/1.ts",
		"http://provider/play/token/live", "http://provider/hls/token/segment",
		"file:///live/user/pass/1.ts", "http://provider/live/user/pass/extra/A.ts",
	} {
		if relayAccountKey(rawURL) != "" {
			t.Errorf("unknown account URL recognized: %s", rawURL)
		}
	}
}

func TestRelayAccountPolicyOptOutAndUnknownURLs(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		enabled    bool
	}{
		{"opt out", "/live/user/pass/", false},
		{"unknown account", "/", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRelayFixture(t)
			cfg := testRelayConfig()
			cfg.ExistingChannelWins = tc.enabled
			m := testRelayManager(t, cfg)
			first := subscribeRelay(t, m, f.server.URL+tc.path+"A.ts", nil)
			second := subscribeRelay(t, m, f.server.URL+tc.path+"B.ts", nil)
			if first.session == second.session || atomic.LoadInt32(&f.calls) != 2 {
				t.Fatal("policy changed unprotected channel selection")
			}
		})
	}
}

func TestRelayOccupiedAccountReconnectRetainsChannel(t *testing.T) {
	f := newRelayFixture(t)
	cfg := testRelayConfig()
	cfg.ExistingChannelWins = true
	cfg.ReconnectInitial = 100 * time.Millisecond
	cfg.ReconnectMax = 100 * time.Millisecond
	m := testRelayManager(t, cfg)
	first := subscribeRelay(t, m, f.server.URL+"/live/user/pass/A.ts", nil)
	receiveChunk(t, first)
	feed := openedFeed(t, f)
	sendFeed(t, feed, nil)
	eventually(t, func() bool { return m.stats().Reconnects > 0 })
	second := subscribeRelay(t, m, f.server.URL+"/live/user/pass/B.ts", nil)
	if second.session != first.session {
		t.Fatal("upstream reconnect allowed a competing channel")
	}
	if got := receiveChunk(t, second); !bytes.Equal(got, tsPackets(0, 2)) {
		t.Fatal("substituted viewer did not resume after upstream reconnect")
	}
	if atomic.LoadInt32(&f.calls) != 2 {
		t.Fatal("unexpected upstream opened alongside reconnection")
	}
}

func TestRelayOccupiedAccountHTTPSubstitutionAndDirectProtection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var callsA, callsB int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := byte(11)
		if r.URL.Path == "/live/up/pass/A.ts" {
			atomic.AddInt32(&callsA, 1)
		} else {
			atomic.AddInt32(&callsB, 1)
			value = 22
		}
		w.Header().Set("Content-Type", "video/mp2t")
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := w.Write(tsPackets(value, 32)); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}))
	t.Cleanup(upstream.Close)
	cfg := testRelayConfig()
	cfg.ExistingChannelWins = true
	m := testRelayManager(t, cfg)
	c := &Config{
		ProxyConfig: &config.ProxyConfig{
			User: "viewer", Password: "secret", XtreamUser: "up", XtreamPassword: "pass",
			XtreamBaseURL: upstream.URL,
		},
		relay: m, endpointAntiColision: "tracks",
		playlist: &m3u.Playlist{Tracks: []m3u.Track{
			{URI: upstream.URL + "/live/up/pass/A.ts", Length: -1},
		}},
	}
	router := gin.New()
	c.m3uRoutes(router.Group("/"))
	c.xtreamRoutes(router.Group("/"))
	router.GET("/status", c.status)
	proxy := relayHTTPServer(t, router)
	t.Cleanup(m.Close)
	client := proxy.Client()
	client.Timeout = 5 * time.Second
	first, err := client.Get(proxy.URL + "/tracks/viewer/secret/0/A.ts")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	second, err := client.Get(proxy.URL + "/live/viewer/secret/B.ts")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	if second.Header.Get("X-IPTV-Relay-Substituted") != "true" {
		t.Fatal("substitution was not indicated")
	}
	for _, response := range []*http.Response{first, second} {
		if got := readHTTPPackets(t, response, 32); !bytes.Equal(got, tsPackets(11, 32)) {
			t.Fatal("requesting B did not receive channel A bytes")
		}
	}
	statusResp, err := client.Get(proxy.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	var state statusResponse
	err = json.NewDecoder(statusResp.Body).Decode(&state)
	statusResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveConnections != 2 || state.Relay == nil || state.Relay.Substitutions != 1 || state.Relay.Upstreams != 1 {
		t.Fatalf("incorrect substitution status: %+v", state)
	}
	substituted := 0
	for _, conn := range state.Connections {
		if conn.Substituted {
			substituted++
			if conn.RequestedURL == "" || conn.URL == conn.RequestedURL {
				t.Fatal("requested and actual relay not distinguished")
			}
		}
		if conn.URL != state.Connections[0].URL {
			t.Fatal("status reported different served sessions")
		}
	}
	if substituted != 1 {
		t.Fatal("substituted viewer missing from status")
	}
	for _, path := range []string{"/live/viewer/secret/B.ts", "/live/viewer/secret/B.m3u8", "/movie/viewer/secret/B.ts"} {
		req, _ := http.NewRequest(http.MethodGet, proxy.URL+path, nil)
		req.Header.Set("Range", "bytes=188-")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("direct bypass %s returned %d", path, response.StatusCode)
		}
	}

	if atomic.LoadInt32(&callsA) != 1 || atomic.LoadInt32(&callsB) != 0 {
		t.Fatal("a B request reached the provider")
	}
}

func TestRelayAccountIdleSwitchWaitsForBodyClose(t *testing.T) {
	closing, unblock := make(chan struct{}), make(chan struct{})
	var calls int32
	cfg := testRelayConfig()
	cfg.ExistingChannelWins = true
	cfg.IdleTimeout = time.Minute
	m := testRelayManager(t, cfg)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(unblock) }) })
	m.client = &http.Client{Transport: relayRoundTripper(func(r *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &closingRelayBody{
				Reader: bytes.NewReader(tsPackets(1, 2)), ctx: r.Context(), closing: closing, unblock: unblock,
			}}, nil
		}
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}
	first := subscribeRelay(t, m, "http://provider/live/user/pass/A.ts", nil)
	first.release()
	done := make(chan int, 1)
	go func() {
		_, code := m.subscribe(context.Background(), "http://provider/live/user/pass/B.ts", nil)
		done <- code
	}()
	select {
	case <-closing:
	case <-time.After(time.Second):
		t.Fatal("new channel did not close the idle upstream")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatal("new channel started before idle upstream closed")
	}
	once.Do(func() { close(unblock) })
	select {
	case code := <-done:
		if code != http.StatusUnauthorized || atomic.LoadInt32(&calls) != 2 {
			t.Fatal("new channel failed after old upstream closed")
		}
	case <-time.After(time.Second):
		t.Fatal("new channel remained stuck after upstream close")
	}
}
