package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func catalogueConfig(sources ...string) *config.ProxyConfig {
	p := config.DefaultPlaylistConfig()
	p.StableIDs = true
	p.FetchTimeout = time.Second
	for i := range sources {
		p.SourceIDs = append(p.SourceIDs, fmt.Sprintf("source%d", i))
	}
	r := config.DefaultRelayConfig()
	r.Enabled = false
	return &config.ProxyConfig{Playlist: &p, Relay: &r, M3USources: sources,
		HostConfig:     &config.HostConfiguration{Hostname: "proxy.example", Port: 8080},
		AdvertisedPort: 80, User: "proxy-user", Password: "proxy-pass", CustomId: "fixed", M3UFileName: "list.m3u"}
}

func catalogueRouter(c *Config) *gin.Engine {
	r := gin.New()
	c.routes(r.Group("/"))
	return r
}

func cataloguePlaylist(t *testing.T, r http.Handler) string {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/list.m3u?username=proxy-user&password=proxy-pass", nil))
	if w.Code != 200 {
		t.Fatalf("playlist: %d %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func catalogueLinks(body string) map[string]string {
	links := make(map[string]string)
	name := ""
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#EXTINF:") {
			name = strings.TrimSpace(line[strings.LastIndex(line, ",")+1:])
		} else if strings.HasPrefix(line, "http") {
			u, _ := url.Parse(line)
			links[name] = u.RequestURI()
		}
	}
	return links
}

func TestCatalogueAtomicRefreshStableRoutes(t *testing.T) {
	var mu sync.Mutex
	entries := []string{"1", "2"}
	fail := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/list" {
			w.Write([]byte("video:" + r.URL.Path))
			return
		}
		if fail {
			w.WriteHeader(503)
			return
		}
		fmt.Fprintln(w, "#EXTM3U")
		for _, id := range entries {
			fmt.Fprintf(w, "#EXTINF:-1,Channel%s\nhttp://%s/live/u/p/%s.ts\n", id, r.Host, id)
		}
	}))
	defer upstream.Close()
	c, err := NewServer(catalogueConfig(upstream.URL + "/list"))
	if err != nil {
		t.Fatal(err)
	}
	r := catalogueRouter(c)
	before := catalogueLinks(cataloguePlaylist(t, r))
	if !strings.Contains(before["Channel1"], "/s") || len(strings.Split(before["Channel1"], "/")[4]) != 65 {
		t.Fatalf("not a stable token: %v", before)
	}
	mu.Lock()
	entries = []string{"3", "2", "1"}
	mu.Unlock()
	if err := c.RefreshCatalogue(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := catalogueLinks(cataloguePlaylist(t, r))
	if before["Channel1"] != after["Channel1"] || before["Channel2"] != after["Channel2"] {
		t.Fatal("stable links changed on reorder")
	}
	mu.Lock()
	fail = true
	mu.Unlock()
	if err := c.RefreshCatalogue(context.Background()); err == nil {
		t.Fatal("failed source accepted")
	}
	if !reflect.DeepEqual(after, catalogueLinks(cataloguePlaylist(t, r))) {
		t.Fatal("failed refresh replaced snapshot")
	}
	mu.Lock()
	fail = false
	entries = []string{"2"}
	mu.Unlock()
	if err := c.RefreshCatalogue(context.Background()); err != nil {
		t.Fatal(err)
	}
	for route, want := range map[string]int{before["Channel1"]: 404, before["Channel2"]: 200, "/fixed/proxy-user/proxy-pass/0/2.ts": 410} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != want {
			t.Fatalf("%s: %d want %d", route, w.Code, want)
		}
	}
}

func TestStartupRetainsSuccessfulSourcesAndCancels(t *testing.T) {
	var a, b int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			atomic.AddInt32(&a, 1)
		} else if atomic.AddInt32(&b, 1) == 1 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,One\nhttp://provider.example/live/u/p/1.ts\n")
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL+"/a", upstream.URL+"/b")
	conf.Playlist.StartupTimeout = 7 * time.Second
	if _, err := NewServerContext(context.Background(), conf); err != nil {
		t.Fatal(err)
	}
	if a != 1 || b != 2 {
		t.Fatalf("fetch counts a=%d b=%d", a, b)
	}

	conf = catalogueConfig(upstream.URL + "/b")
	conf.Playlist.StartupTimeout = time.Minute
	atomic.StoreInt32(&b, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := NewServerContext(ctx, conf); err == nil || time.Since(start) > time.Second {
		t.Fatalf("startup cancellation: %v", err)
	}
}

func TestCatalogueAccountIdentityChangeRejected(t *testing.T) {
	var password atomic.Value
	password.Store("old-secret")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1,One\nhttp://provider.example/live/u/%s/1.ts\n", password.Load())
	}))
	defer upstream.Close()
	c, err := NewServer(catalogueConfig(upstream.URL))
	if err != nil {
		t.Fatal(err)
	}

	r := catalogueRouter(c)
	before := cataloguePlaylist(t, r)
	password.Store("new-secret")
	if err := c.RefreshCatalogue(context.Background()); err == nil || err.Error() != "account_identity_changed" {
		t.Fatalf("identity change: %v", err)
	}
	if before != cataloguePlaylist(t, r) {
		t.Fatal("account change published")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/status", nil))
	var status struct {
		Catalogue struct {
			Ready      bool   `json:"ready"`
			Generation uint64 `json:"generation"`
			Error      string `json:"last_refresh_error_kind"`
		} `json:"catalogue"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Catalogue.Ready || status.Catalogue.Generation != 1 || status.Catalogue.Error != "account_identity_changed" {
		t.Fatalf("status %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "provider.example") {
		t.Fatal("status leaks source")
	}
}

func TestCatalogueRejectsMixedAccountsEvenInExcludedGroups(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1 group-title=\"Keep\",One\nhttp://provider.example/live/u/p/1.ts\n#EXTINF:-1 group-title=\"Skip\",Two\nhttp://provider.example/live/other/p/2.ts\n")
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL)
	conf.IncludeGroups = []string{"Keep"}
	if _, err := NewServer(conf); err == nil {
		t.Fatal("source account validation ignored excluded records")
	}
}
