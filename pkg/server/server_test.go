package server

import (
	"net/url"
	"testing"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestReplaceURLOmitsDefaultHTTPSPort(t *testing.T) {
	cfg := &Config{
		ProxyConfig: &config.ProxyConfig{
			HostConfig: &config.HostConfiguration{
				Hostname: "example.com",
				Port:     9000,
			},
			AdvertisedPort: 443,
			HTTPS:          true,
			User:           config.CredentialString("user"),
			Password:       config.CredentialString("pass"),
		},
		endpointAntiColision: "anti",
	}

	got, err := cfg.replaceURL("http://upstream.example/live/stream.ts", 7, false)
	if err != nil {
		t.Fatalf("replaceURL() error = %v", err)
	}

	want := "https://example.com/anti/user/pass/7/stream.ts"
	if got != want {
		t.Fatalf("replaceURL() = %q, want %q", got, want)
	}
}

func TestReplaceURLKeepsNonDefaultHTTPSPort(t *testing.T) {
	cfg := &Config{
		ProxyConfig: &config.ProxyConfig{
			HostConfig: &config.HostConfiguration{
				Hostname: "example.com",
				Port:     9000,
			},
			AdvertisedPort: 9443,
			HTTPS:          true,
			User:           config.CredentialString("user"),
			Password:       config.CredentialString("pass"),
		},
		endpointAntiColision: "anti",
	}

	got, err := cfg.replaceURL("http://upstream.example/live/stream.ts", 7, false)
	if err != nil {
		t.Fatalf("replaceURL() error = %v", err)
	}

	want := "https://example.com:9443/anti/user/pass/7/stream.ts"
	if got != want {
		t.Fatalf("replaceURL() = %q, want %q", got, want)
	}
}

func TestReplaceURLOmitsDefaultHTTPPort(t *testing.T) {
	cfg := &Config{
		ProxyConfig: &config.ProxyConfig{
			HostConfig: &config.HostConfiguration{
				Hostname: "example.com",
				Port:     8080,
			},
			AdvertisedPort: 80,
			HTTPS:          false,
			User:           config.CredentialString("user"),
			Password:       config.CredentialString("pass"),
		},
		endpointAntiColision: "anti",
	}

	got, err := cfg.replaceURL("http://upstream.example/live/stream.ts", 7, false)
	if err != nil {
		t.Fatalf("replaceURL() error = %v", err)
	}

	want := "http://example.com/anti/user/pass/7/stream.ts"
	if got != want {
		t.Fatalf("replaceURL() = %q, want %q", got, want)
	}
}

func TestReplaceURLPreservesXtreamCredentialSwapWithoutDefaultHTTPSPort(t *testing.T) {
	cfg := &Config{
		ProxyConfig: &config.ProxyConfig{
			HostConfig: &config.HostConfiguration{
				Hostname: "example.com",
				Port:     9000,
			},
			AdvertisedPort: 443,
			HTTPS:          true,
			User:           config.CredentialString("proxy-user"),
			Password:       config.CredentialString("proxy-pass"),
			XtreamUser:     config.CredentialString("up-user"),
			XtreamPassword: config.CredentialString("up-pass"),
		},
	}

	got, err := cfg.replaceURL("http://upstream.example/live/up-user/up-pass/123.ts", 0, true)
	if err != nil {
		t.Fatalf("replaceURL() error = %v", err)
	}

	wantURL, _ := url.Parse("https://example.com/live/proxy-user/proxy-pass/123.ts")
	if got != wantURL.String() {
		t.Fatalf("replaceURL() = %q, want %q", got, wantURL.String())
	}
}

func TestTrackPathBaseStripsQueryString(t *testing.T) {
	base, err := trackPathBase("http://provider.example/live/stream.ts?token=abc")
	if err != nil {
		t.Fatalf("trackPathBase() error = %v", err)
	}

	if base != "stream.ts" {
		t.Fatalf("trackPathBase() = %q, want %q", base, "stream.ts")
	}
}
