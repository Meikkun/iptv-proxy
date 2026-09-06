package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestPlaylistFetchTimeoutIncludesBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("#EXTM3U\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	start := time.Now()
	_, _, err := loadPlaylistSourceContext(context.Background(), upstream.URL, nil, 40*time.Millisecond)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("body fetch was not bounded: %v (%s)", err, time.Since(start))
	}
}

func TestParserFilteringValidatesExcludedRecords(t *testing.T) {
	for _, input := range []string{
		"", "#EXTM3U\n", "#EXTM3U\n#EXTINF:-1 group-title=\"Skip\",Bad\n",
		"#EXTM3U\n#EXTINF:-1 group-title=\"Skip\",Bad\nhttp://a/%zz\n#EXTINF:-1 group-title=\"Keep\",Good\nhttp://a/good.ts\n",
	} {
		if _, _, err := parsePlaylistFiltered(strings.NewReader(input), []string{"Keep"}, nil); err == nil {
			t.Fatalf("accepted unusable playlist %q", input)
		}
	}
}

func TestParserFilteringPreservesMatching(t *testing.T) {
	input := "#EXTM3U\n"
	for _, group := range []string{"ES|News", "Sports", "sports", "日本", "News|HD"} {
		input += "#EXTINF:-1 group-title=\"" + group + "\",Name\nhttp://provider.example/a.ts\n"
	}
	all, err := parsePlaylist(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, groups := range [][]string{{"Sports"}, {"ES|*"}, {"日?"}, {"News|HD"}, {"sports"}, {"no-match"}} {
		want, wantErr := filterPlaylistByGroups(all, groups)
		got, discovered, err := parsePlaylistFiltered(strings.NewReader(input), groups, nil)
		if (wantErr == nil) != (err == nil) || (err == nil && !reflect.DeepEqual(want.Tracks, got.Tracks)) {
			t.Fatalf("filter %v: got %+v, %v; want %+v, %v", groups, got, err, want, wantErr)
		}
		if err == nil && !reflect.DeepEqual(discovered, playlistGroups(all)) {
			t.Fatal("lost discovered groups")
		}
	}
}

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

func TestFilterPlaylistByGroupsSupportsWildcardsAndKeepsOriginalGroups(t *testing.T) {
	playlist := m3u.Playlist{
		Tracks: []m3u.Track{
			{
				Name: "Sports ES One",
				Tags: []m3u.Tag{{Name: groupTitleTagName, Value: "Sports ES"}},
			},
			{
				Name: "Sports HD One",
				Tags: []m3u.Tag{{Name: groupTitleTagName, Value: "Sports HD"}},
			},
			{
				Name: "Sports US One",
				Tags: []m3u.Tag{{Name: groupTitleTagName, Value: "Sports US"}},
			},
			{
				Name: "News Local One",
				Tags: []m3u.Tag{{Name: groupTitleTagName, Value: "News Local"}},
			},
		},
	}

	filtered, err := filterPlaylistByGroups(playlist, []string{"Sports*"})
	if err != nil {
		t.Fatalf("filterPlaylistByGroups() error = %v", err)
	}

	gotGroups := []string{
		trackGroup(filtered.Tracks[0]),
		trackGroup(filtered.Tracks[1]),
		trackGroup(filtered.Tracks[2]),
	}
	wantGroups := []string{"Sports ES", "Sports HD", "Sports US"}
	if !reflect.DeepEqual(gotGroups, wantGroups) {
		t.Fatalf("filtered groups = %v, want %v", gotGroups, wantGroups)
	}
}

func TestMarshallIntoPreservesOriginalGroupTitlesAfterWildcardFiltering(t *testing.T) {
	cfg := &config.ProxyConfig{
		HostConfig: &config.HostConfiguration{
			Hostname: "localhost",
			Port:     8080,
		},
		User:           config.CredentialString("user"),
		Password:       config.CredentialString("pass"),
		AdvertisedPort: 8080,
	}

	server := &Config{
		ProxyConfig: cfg,
		playlist: &m3u.Playlist{
			Tracks: []m3u.Track{
				{
					Name:   "Sports ES One",
					Length: -1,
					URI:    "http://provider.example/one.ts",
					Tags:   []m3u.Tag{{Name: groupTitleTagName, Value: "Sports ES"}},
				},
				{
					Name:   "Sports HD One",
					Length: -1,
					URI:    "http://provider.example/two.ts",
					Tags:   []m3u.Tag{{Name: groupTitleTagName, Value: "Sports HD"}},
				},
			},
		},
		endpointAntiColision: "stable",
	}

	tmpFile, err := os.CreateTemp("", "iptv-proxy-marshall-*.m3u")
	if err != nil {
		t.Fatalf("os.CreateTemp() error = %v", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if err := server.marshallInto(tmpFile, false); err != nil {
		t.Fatalf("marshallInto() error = %v", err)
	}

	content, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}

	gotContent := string(content)
	if !strings.Contains(gotContent, `group-title="Sports ES"`) {
		t.Fatalf("output missing original group title Sports ES: %s", gotContent)
	}

	if !strings.Contains(gotContent, `group-title="Sports HD"`) {
		t.Fatalf("output missing original group title Sports HD: %s", gotContent)
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

	output := httptest.NewRecorder()
	ctx, _ := createTestContext(output)
	server.getM3U(ctx)
	selected, err := parsePlaylist(strings.NewReader(output.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Tracks) != 2 {
		t.Fatalf("NewServer() filtered track count = %d, want 2", len(selected.Tracks))
	}

	gotGroups := server.Groups()
	wantGroups := []string{"Movies", "News", "Sports"}
	if !reflect.DeepEqual(gotGroups, wantGroups) {
		t.Fatalf("NewServer() groups = %v, want %v", gotGroups, wantGroups)
	}

	gotNames := []string{selected.Tracks[0].Name, selected.Tracks[1].Name}
	wantNames := []string{"Alpha Sports", "Beta Sports"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("NewServer() filtered tracks = %v, want %v", gotNames, wantNames)
	}
}

func TestParsePlaylistSupportsBOMAndLeadingWhitespaceHeader(t *testing.T) {
	content := "\uFEFF   #EXTM3U\n#EXTINF:   -1    tvg-id=\"id-1\", Channel One\nhttp://provider.example/one.ts\n"

	playlist, err := parsePlaylist(strings.NewReader(content))
	if err != nil {
		t.Fatalf("parsePlaylist() error = %v", err)
	}

	if len(playlist.Tracks) != 1 {
		t.Fatalf("parsePlaylist() tracks = %d, want 1", len(playlist.Tracks))
	}

	if playlist.Tracks[0].Length != -1 {
		t.Fatalf("track length = %d, want -1", playlist.Tracks[0].Length)
	}
}

func TestParseTrackMetadataHandlesExtraWhitespace(t *testing.T) {
	track, err := parseTrackMetadata("#EXTINF:   -1    tvg-id=\"id-2\"   ,  Channel Two")
	if err != nil {
		t.Fatalf("parseTrackMetadata() error = %v", err)
	}

	if track.Length != -1 {
		t.Fatalf("parseTrackMetadata() length = %d, want -1", track.Length)
	}

	if track.Name != "Channel Two" {
		t.Fatalf("parseTrackMetadata() name = %q, want %q", track.Name, "Channel Two")
	}
}

func TestParsePlaylistFailsWhenTrackURIIsMissing(t *testing.T) {
	_, err := parsePlaylist(strings.NewReader("#EXTM3U\n#EXTINF:-1,No URI\n"))
	if err == nil {
		t.Fatal("parsePlaylist() error = nil, want non-nil")
	}

	if !strings.Contains(err.Error(), "missing uri for track") {
		t.Fatalf("parsePlaylist() error = %q, want missing uri error", err.Error())
	}
}
