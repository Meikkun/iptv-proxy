package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogueUnusableRefreshPreservesMemoryAndDisk(t *testing.T) {
	for _, bad := range []string{
		"#EXTM3U\n",
		"#EXTM3U\n#EXTINF:-1,Truncated\n",
		"#EXTM3U\n#EXTINF:-1 group-title=\"Skip\",Excluded\nhttp://provider.example/%zz\n",
		"#EXTM3U\n#EXTINF:-1 group-title=\"Other\",No match\nhttp://provider.example/live/u/p/1.ts\n",
	} {
		t.Run(fmt.Sprintf("invalid_%d", len(bad)), func(t *testing.T) {
			var body atomic.Value
			body.Store("#EXTM3U\n#EXTINF:-1 group-title=\"Keep\",Good\nhttp://provider.example/live/u/p/1.ts\n")
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body.Load()) }))
			defer upstream.Close()
			conf := catalogueConfig(upstream.URL)
			conf.IncludeGroups = []string{"Keep"}
			conf.Playlist.StateDir = t.TempDir()
			c, err := NewServer(conf)
			if err != nil {
				t.Fatal(err)
			}
			before := cataloguePlaylist(t, catalogueRouter(c))
			filename := filepath.Join(conf.Playlist.StateDir, "catalogue.json")
			diskBefore, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			body.Store(bad)
			if err := c.RefreshCatalogue(context.Background()); err == nil {
				t.Fatal("unusable refresh accepted")
			}
			if before != cataloguePlaylist(t, catalogueRouter(c)) {
				t.Fatal("unusable refresh changed memory")
			}
			diskAfter, err := os.ReadFile(filename)
			if err != nil || string(diskAfter) != string(diskBefore) {
				t.Fatal("unusable refresh changed disk")
			}
			restored, err := NewServer(conf)
			if err != nil || before != cataloguePlaylist(t, catalogueRouter(restored)) {
				t.Fatalf("last-good restore failed: %v", err)
			}
		})
	}
}

func TestPlaylistGlobalStartupBudgetBoundsBodyFetch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "#EXTM3U")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL)
	conf.Playlist.FetchTimeout = time.Second
	conf.Playlist.StartupTimeout = 40 * time.Millisecond
	start := time.Now()
	if _, err := NewServerContext(context.Background(), conf); err == nil {
		t.Fatal("body timeout accepted empty startup")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("global deadline was ignored: %s", elapsed)
	}
}

func TestCatalogueRejectsStateSymlinkWithoutOverwritingTarget(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,One\nhttp://provider.example/live/u/p/1.ts\n")
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL)
	conf.Playlist.StateDir = t.TempDir()
	target := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(target, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(conf.Playlist.StateDir, "catalogue.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(conf); err == nil {
		t.Fatal("symlink state accepted")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "private" {
		t.Fatal("state symlink target overwritten")
	}
}
