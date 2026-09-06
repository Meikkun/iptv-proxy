package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestStableTrackIdentityRules(t *testing.T) {
	var body atomic.Value
	body.Store("#EXTM3U\n#EXTINF:-1,Channel\nhttp://first.example/live/u/first-secret/123.ts\n")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body.Load()) }))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL)
	first, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	before := catalogueLinks(cataloguePlaylist(t, catalogueRouter(first)))
	body.Store("#EXTM3U\n#EXTINF:-1,Channel\nhttp://second.example/live/u/second-secret/123.ts\n")
	second, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, catalogueLinks(cataloguePlaylist(t, catalogueRouter(second)))) {
		t.Fatal("recognized identity depended on password or hostname")
	}
	conf.Playlist.SourceIDs = []string{"other"}
	third, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, catalogueLinks(cataloguePlaylist(t, catalogueRouter(third)))) {
		t.Fatal("source IDs did not isolate channels")
	}

	body.Store("#EXTM3U\n#EXTINF:-1,Channel\nhttp://provider.example/opaque.ts?token=one#first\n")
	fallback, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	before = catalogueLinks(cataloguePlaylist(t, catalogueRouter(fallback)))
	body.Store("#EXTM3U\n#EXTINF:-1,Channel\nhttp://provider.example/opaque.ts?token=one#second\n")
	if err := fallback.RefreshCatalogue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, catalogueLinks(cataloguePlaylist(t, catalogueRouter(fallback)))) {
		t.Fatal("fallback identity included fragment")
	}
	body.Store("#EXTM3U\n#EXTINF:-1,Channel\nhttp://provider.example/opaque.ts?token=two\n")
	if err := fallback.RefreshCatalogue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, catalogueLinks(cataloguePlaylist(t, catalogueRouter(fallback)))) {
		t.Fatal("fallback identity discarded query")
	}
}

func TestStableTrackConflictsRejectWholeCandidate(t *testing.T) {
	var suffix atomic.Value
	suffix.Store("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,One\nhttp://provider.example/live/u/p/1.ts\n")
		fmt.Fprintf(w, "#EXTINF:-1,Duplicate\nhttp://provider.example/live/u/p/1.ts%s\n", suffix.Load())
	}))
	defer upstream.Close()
	c, err := NewServer(catalogueConfig(upstream.URL))
	if err != nil {
		t.Fatal(err)
	}
	before := cataloguePlaylist(t, catalogueRouter(c))
	links := catalogueLinks(before)
	if links["One"] != links["Duplicate"] {
		t.Fatal("identical upstream duplicates must share a route")
	}
	suffix.Store("?variant=other")
	if err := c.RefreshCatalogue(context.Background()); err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
	if before != cataloguePlaylist(t, catalogueRouter(c)) {
		t.Fatal("conflict changed catalogue")
	}
}

func TestStableTrackNumericAndSingleSourceXtreamRoutes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,One\nhttp://provider.example/live/u/p/1.ts\n")
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL + "/get.php?username=u&password=p")
	conf.XtreamBaseURL = upstream.URL
	conf.XtreamUser, conf.XtreamPassword = "u", "p"
	conf.RemoteURL, _ = url.Parse(conf.M3USources[0])
	c, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	r := catalogueRouter(c)
	if !strings.Contains(cataloguePlaylist(t, r), "/s") {
		t.Fatal("stable source bypassed catalogue through Xtream auto mode")
	}
	for _, token := range []string{"0", "1844674407370955161600000"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/fixed/proxy-user/proxy-pass/"+token+"/1.ts", nil))
		if w.Code != 410 {
			t.Fatalf("numeric %s: got %d want 410", token, w.Code)
		}
	}
}
