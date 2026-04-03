package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestLoadPlaylistSourcesMergesTracksAndPreservesGroups(t *testing.T) {
	playlist, err := loadPlaylistSources([]string{
		filepath.Join("testdata", "source_a.m3u"),
		filepath.Join("testdata", "source_b.m3u"),
	})
	if err != nil {
		t.Fatalf("loadPlaylistSources() error = %v", err)
	}

	if len(playlist.Tracks) != 4 {
		t.Fatalf("loadPlaylistSources() track count = %d, want 4", len(playlist.Tracks))
	}

	gotNames := []string{
		playlist.Tracks[0].Name,
		playlist.Tracks[1].Name,
		playlist.Tracks[2].Name,
		playlist.Tracks[3].Name,
	}
	wantNames := []string{"Alpha News", "Alpha Sports", "Beta Movies", "Beta Sports"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("track order = %v, want %v", gotNames, wantNames)
	}

	gotGroups := []string{
		trackGroup(playlist.Tracks[0]),
		trackGroup(playlist.Tracks[1]),
		trackGroup(playlist.Tracks[2]),
		trackGroup(playlist.Tracks[3]),
	}
	wantGroups := []string{"News", "Sports", "Movies", "Sports"}
	if !reflect.DeepEqual(gotGroups, wantGroups) {
		t.Fatalf("track groups = %v, want %v", gotGroups, wantGroups)
	}
}

func TestFilterPlaylistByGroups(t *testing.T) {
	playlist, err := loadPlaylistSources([]string{
		filepath.Join("testdata", "source_a.m3u"),
		filepath.Join("testdata", "source_b.m3u"),
	})
	if err != nil {
		t.Fatalf("loadPlaylistSources() error = %v", err)
	}

	filtered, err := filterPlaylistByGroups(playlist, []string{"Sports", "Movies"})
	if err != nil {
		t.Fatalf("filterPlaylistByGroups() error = %v", err)
	}

	gotNames := []string{
		filtered.Tracks[0].Name,
		filtered.Tracks[1].Name,
		filtered.Tracks[2].Name,
	}
	wantNames := []string{"Alpha Sports", "Beta Movies", "Beta Sports"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("filtered track names = %v, want %v", gotNames, wantNames)
	}
}

func TestFilterPlaylistByGroupsReturnsAvailableGroupsOnNoMatch(t *testing.T) {
	playlist, err := loadPlaylistSources([]string{
		filepath.Join("testdata", "source_a.m3u"),
		filepath.Join("testdata", "source_b.m3u"),
	})
	if err != nil {
		t.Fatalf("loadPlaylistSources() error = %v", err)
	}

	_, err = filterPlaylistByGroups(playlist, []string{"Kids"})
	if err == nil {
		t.Fatal("filterPlaylistByGroups() error = nil, want non-nil")
	}

	if !strings.Contains(err.Error(), "available groups: Movies, News, Sports") {
		t.Fatalf("filterPlaylistByGroups() error = %q, want available groups list", err)
	}
}

func TestLoadPlaylistSourceResolvesRelativeRemoteURIs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/playlist.m3u" {
			http.NotFound(w, r)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 group-title=\"News\",Relative One\nrelative/one.ts\n#EXTINF:-1 group-title=\"Sports\",Relative Two\n/absolute/two.ts\n"))
	}))
	defer server.Close()

	playlist, err := loadPlaylistSource(server.URL + "/playlist.m3u")
	if err != nil {
		t.Fatalf("loadPlaylistSource() error = %v", err)
	}

	gotURIs := []string{playlist.Tracks[0].URI, playlist.Tracks[1].URI}
	wantURIs := []string{server.URL + "/relative/one.ts", server.URL + "/absolute/two.ts"}
	if !reflect.DeepEqual(gotURIs, wantURIs) {
		t.Fatalf("resolved uris = %v, want %v", gotURIs, wantURIs)
	}
}

func TestNewServerAppliesGroupFilteringAndRetainsAvailableGroups(t *testing.T) {
	cfg := &config.ProxyConfig{
		HostConfig: &config.HostConfiguration{
			Hostname: "localhost",
			Port:     8080,
		},
		M3USources: []string{
			filepath.Join("testdata", "source_a.m3u"),
			filepath.Join("testdata", "source_b.m3u"),
		},
		IncludeGroups: []string{"Sports"},
		RemoteURL:     &url.URL{},
	}

	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	if len(server.playlist.Tracks) != 2 {
		t.Fatalf("NewServer() filtered track count = %d, want 2", len(server.playlist.Tracks))
	}

	gotGroups := server.Groups()
	wantGroups := []string{"Movies", "News", "Sports"}
	if !reflect.DeepEqual(gotGroups, wantGroups) {
		t.Fatalf("NewServer() groups = %v, want %v", gotGroups, wantGroups)
	}

	gotNames := []string{server.playlist.Tracks[0].Name, server.playlist.Tracks[1].Name}
	wantNames := []string{"Alpha Sports", "Beta Sports"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("NewServer() filtered tracks = %v, want %v", gotNames, wantNames)
	}
}
