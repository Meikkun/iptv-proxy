package cmd

import (
	"reflect"
	"testing"
)

func TestNormalizeStringSlice(t *testing.T) {
	got := normalizeStringSlice([]string{" http://a.example/list.m3u | http://b.example/list.m3u ", "http://c.example/list.m3u"})
	want := []string{"http://a.example/list.m3u", "http://b.example/list.m3u", "http://c.example/list.m3u"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeStringSlice() = %v, want %v", got, want)
	}
}

func TestValidateXtreamSourceConfig(t *testing.T) {
	tests := []struct {
		name          string
		m3uSources    []string
		xtreamUser    string
		xtreamPass    string
		xtreamBaseURL string
		wantErr       bool
	}{
		{
			name:          "single source with xtream config",
			m3uSources:    []string{"http://provider.example/get.php"},
			xtreamUser:    "user",
			xtreamPass:    "pass",
			xtreamBaseURL: "http://provider.example",
			wantErr:       false,
		},
		{
			name:       "multiple sources without xtream config",
			m3uSources: []string{"http://a.example/list.m3u", "http://b.example/list.m3u"},
			wantErr:    false,
		},
		{
			name:          "multiple sources with xtream config",
			m3uSources:    []string{"http://a.example/list.m3u", "http://b.example/list.m3u"},
			xtreamBaseURL: "http://provider.example",
			wantErr:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateXtreamSourceConfig(tt.m3uSources, tt.xtreamUser, tt.xtreamPass, tt.xtreamBaseURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateXtreamSourceConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
