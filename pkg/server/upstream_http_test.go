package server

import (
	"net/http"
	"testing"
)

func TestNewStreamingHTTPClientDoesNotUseTotalRequestTimeout(t *testing.T) {
	client := newStreamingHTTPClient()

	if client.Timeout != 0 {
		t.Fatalf("newStreamingHTTPClient() timeout = %v, want 0", client.Timeout)
	}
}

func TestNewStreamingHTTPClientUsesResponseHeaderTimeout(t *testing.T) {
	client := newStreamingHTTPClient()

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("newStreamingHTTPClient() transport type = %T, want *http.Transport", client.Transport)
	}

	if transport.ResponseHeaderTimeout != defaultUpstreamRequestTimeout {
		t.Fatalf(
			"newStreamingHTTPClient() response header timeout = %v, want %v",
			transport.ResponseHeaderTimeout,
			defaultUpstreamRequestTimeout,
		)
	}
}
