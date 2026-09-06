package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCatalogueStateRestoresPrivateMetadataWithCurrentOutputConfig(t *testing.T) {
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,One\nhttp://provider.example/live/up-user/up-secret/1.ts\n")
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL)
	conf.Playlist.StateDir = filepath.Join(t.TempDir(), "private")
	if _, err := NewServer(conf); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(conf.Playlist.StateDir, "catalogue.json")
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "up-secret") || strings.Contains(string(raw), "proxy-pass") || strings.Contains(string(raw), "#EXTM3U") {
		t.Fatal("state must contain raw metadata, not public output")
	}
	for path, mode := range map[string]os.FileMode{conf.Playlist.StateDir: 0700, statePath: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("private permissions %s: %v %v", path, info, err)
		}
	}
	conf.HostConfig.Hostname = "new-proxy.example"
	conf.CustomEndpoint = "new-prefix"
	conf.Password = "new-proxy-pass"
	c, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatal("valid restore synchronously refetched providers")
	}
	w := httptest.NewRecorder()
	catalogueRouter(c).ServeHTTP(w, httptest.NewRequest("GET", "/new-prefix/list.m3u?username=proxy-user&password=new-proxy-pass", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "new-proxy.example/new-prefix/fixed/proxy-user/new-proxy-pass/") {
		t.Fatalf("restored output: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "up-secret") {
		t.Fatal("upstream credentials in public output")
	}
}

func TestCatalogueStateFingerprintAndMalformedStateBootstrap(t *testing.T) {
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:-1,One\nhttp://provider.example/live/u/p/1.ts\n")
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL + "?password=old")
	conf.Playlist.StateDir = t.TempDir()
	if _, err := NewServer(conf); err != nil {
		t.Fatal(err)
	}
	conf.M3USources[0] = upstream.URL + "?password=new"
	if _, err := NewServer(conf); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("fingerprint mismatch not refetched")
	}
	if err := os.WriteFile(filepath.Join(conf.Playlist.StateDir, "catalogue.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(conf); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("corrupt state not refetched")
	}
}

func TestCatalogueStateWriteFailureKeepsPreviousSnapshot(t *testing.T) {
	var id atomic.Value
	id.Store("1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n#EXTINF:-1,One\nhttp://provider.example/live/u/p/%s.ts\n", id.Load())
	}))
	defer upstream.Close()
	conf := catalogueConfig(upstream.URL)
	conf.Playlist.StateDir = filepath.Join(t.TempDir(), "state")
	c, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	before := cataloguePlaylist(t, catalogueRouter(c))
	oldPath := conf.Playlist.StateDir + ".saved"
	if err := os.Rename(conf.Playlist.StateDir, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf.Playlist.StateDir, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	id.Store("2")
	if err := c.RefreshCatalogue(context.Background()); err == nil {
		t.Fatal("state write failure accepted")
	}
	if before != cataloguePlaylist(t, catalogueRouter(c)) {
		t.Fatal("write failure published memory snapshot")
	}
	if err := os.Remove(conf.Playlist.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldPath, conf.Playlist.StateDir); err != nil {
		t.Fatal(err)
	}
	restored, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	if before != cataloguePlaylist(t, catalogueRouter(restored)) {
		t.Fatal("write failure replaced persisted state")
	}
}
