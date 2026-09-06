package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCatalogueFilteringPreservesMergedSelection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		group := "Sports"
		if r.URL.Path == "/b" {
			group = "News"
		}
		fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1 group-title=\"%s\",%s\nhttp://provider.example/live/u/p/1.ts\n", group, group)
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL+"/a", upstream.URL+"/b")
	conf.IncludeGroups = []string{"Sports"}
	conf.Playlist.StateDir = t.TempDir()
	c, err := NewServer(conf)
	if err != nil {
		t.Fatalf("healthy source without selected groups rejected: %v", err)
	}
	links := catalogueLinks(cataloguePlaylist(t, catalogueRouter(c)))
	if len(links) != 1 || links["Sports"] == "" || !reflect.DeepEqual(c.Groups(), []string{"News", "Sports"}) {
		t.Fatalf("merged filtering changed: %v, groups %v", links, c.Groups())
	}
	if _, err := NewServer(conf); err != nil {
		t.Fatalf("zero-selected source state could not restore: %v", err)
	}
	conf.IncludeGroups = []string{"Nothing"}
	if _, err := NewServer(conf); err == nil {
		t.Fatal("globally zero-match source set accepted")
	}
}
