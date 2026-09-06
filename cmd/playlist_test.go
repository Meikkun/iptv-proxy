package cmd

import (
	"reflect"
	"testing"
	"time"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
	"github.com/spf13/viper"
)

func TestPlaylistConfigDefaultsAndEnvironment(t *testing.T) {
	bindRelayTestConfig(t)
	for _, name := range []string{"PLAYLIST_FETCH_TIMEOUT", "PLAYLIST_STARTUP_TIMEOUT", "PLAYLIST_REFRESH_INTERVAL", "PLAYLIST_STATE_DIR", "PLAYLIST_STABLE_IDS", "M3U_SOURCE_IDS"} {
		t.Setenv(name, "")
	}
	got, err := resolvePlaylistConfig(nil)
	if err != nil || !reflect.DeepEqual(got, config.DefaultPlaylistConfig()) {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	t.Setenv("PLAYLIST_FETCH_TIMEOUT", "180s")
	t.Setenv("PLAYLIST_STARTUP_TIMEOUT", "600s")
	t.Setenv("PLAYLIST_REFRESH_INTERVAL", "6h")
	t.Setenv("PLAYLIST_STATE_DIR", "/var/lib/iptv-proxy")
	t.Setenv("PLAYLIST_STABLE_IDS", "true")
	t.Setenv("M3U_SOURCE_IDS", "primary|secondary")
	got, err = resolvePlaylistConfig([]string{"a", "b"})
	if err != nil || got.FetchTimeout != 180*time.Second || got.StartupTimeout != 600*time.Second ||
		got.RefreshInterval != 6*time.Hour || !got.StableIDs || got.StateDir != "/var/lib/iptv-proxy" ||
		!reflect.DeepEqual(got.SourceIDs, []string{"primary", "secondary"}) {
		t.Fatalf("environment = %+v, %v", got, err)
	}
	flag := rootCmd.Flags().Lookup("playlist-fetch-timeout")
	old, changed := flag.Value.String(), flag.Changed
	t.Cleanup(func() { flag.Value.Set(old); flag.Changed = changed })
	rootCmd.Flags().Set("playlist-fetch-timeout", "2s")
	got, err = resolvePlaylistConfig([]string{"a", "b"})
	if err != nil || got.FetchTimeout != 2*time.Second {
		t.Fatalf("CLI precedence = %+v, %v", got, err)
	}
}

func TestPlaylistConfigRejectsInvalidValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"playlist-fetch-timeout", "0s"}, {"playlist-fetch-timeout", "-1s"},
		{"playlist-fetch-timeout", "later"}, {"playlist-startup-timeout", "-1s"},
		{"playlist-refresh-interval", "-1s"}, {"playlist-stable-ids", "perhaps"},
		{"playlist-stable-ids", "true"}, {"m3u-source-ids", "one|one"},
		{"m3u-source-ids", "../bad|two"}, {"m3u-source-ids", "one"},
		{"m3u-source-ids", "one||two"}, {"m3u-source-ids", "|two"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			bindRelayTestConfig(t)
			viper.Set(tc.key, tc.value)
			if _, err := resolvePlaylistConfig([]string{"a", "b"}); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
