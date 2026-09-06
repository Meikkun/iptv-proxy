package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogueRefreshPreservesOccupiedRelay(t *testing.T) {
	var streams int32
	var removed int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/list" {
			fmt.Fprintln(w, "#EXTM3U")
			if atomic.LoadInt32(&removed) == 0 {
				fmt.Fprintf(w, "#EXTINF:-1,One\nhttp://%s/live/u/p/1.ts\n", r.Host)
			}
			fmt.Fprintf(w, "#EXTINF:-1,Two\nhttp://%s/live/u/p/2.ts\n", r.Host)
			return
		}
		atomic.AddInt32(&streams, 1)
		w.Header().Set("Content-Type", "video/mp2t")
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
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL + "/list")
	relay := testRelayConfig()
	relay.ExistingChannelWins = true
	conf.Relay = &relay
	c, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	defer c.relay.Close()
	r := catalogueRouter(c)
	proxy := relayHTTPServer(t, r)
	links := catalogueLinks(cataloguePlaylist(t, r))
	viewer, err := proxy.Client().Get(proxy.URL + links["One"])
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Body.Close()
	readHTTPPackets(t, viewer, 2)
	atomic.StoreInt32(&removed, 1)
	if err := c.RefreshCatalogue(context.Background()); err != nil {
		t.Fatal(err)
	}
	readHTTPPackets(t, viewer, 2)
	newViewer, err := proxy.Client().Get(proxy.URL + links["Two"])
	if err != nil {
		t.Fatal(err)
	}
	defer newViewer.Body.Close()
	if packet := readHTTPPackets(t, newViewer, 2); packet[1] != 1 {
		t.Fatal("occupied channel not retained")
	}
	if atomic.LoadInt32(&streams) != 1 {
		t.Fatal("refresh or occupied-account join opened a second upstream")
	}
	removedRequest := httptest.NewRecorder()
	r.ServeHTTP(removedRequest, httptest.NewRequest("GET", links["One"], nil))
	if removedRequest.Code != 404 {
		t.Fatal("removed channel admitted a new viewer")
	}
}

func TestCatalogueConcurrentRefreshReadersAndTriggers(t *testing.T) {
	var active, overlap, calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&active, 1) != 1 {
			atomic.StoreInt32(&overlap, 1)
		}
		defer atomic.AddInt32(&active, -1)
		n := atomic.AddInt32(&calls, 1)
		time.Sleep(3 * time.Millisecond)
		fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1,Generation%dA\nhttp://provider.example/live/u/p/1.ts\n#EXTINF:-1,Generation%dB\nhttp://provider.example/live/u/p/2.ts\n", n, n)
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL)
	conf.Playlist.RefreshInterval = 5 * time.Millisecond
	c, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	r := catalogueRouter(c)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.runCatalogueRefresh(ctx) }()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				c.RequestCatalogueRefresh()
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("GET", "/list.m3u?username=proxy-user&password=proxy-pass", nil))
				body := w.Body.String()
				names := []string{}
				for name := range catalogueLinks(body) {
					names = append(names, strings.TrimRight(name, "AB"))
				}
				if w.Code != 200 || len(names) != 2 || names[0] != names[1] {
					t.Errorf("torn generation: %d %s", w.Code, body)
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	eventually(t, func() bool { return atomic.LoadInt32(&calls) >= 3 })
	cancel()
	<-done
	if overlap != 0 {
		t.Fatal("timer/manual refreshes fetched concurrently")
	}
}
