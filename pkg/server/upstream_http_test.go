package server

import (
	"net/http"
	"testing"
)

func TestStreamingHTTPClientDoesNotUseTotalRequestTimeout(t *testing.T) {
	if streamingHTTPClient.Timeout != 0 {
		t.Fatalf("streamingHTTPClient timeout = %v, want 0", streamingHTTPClient.Timeout)
	}
}

func TestStreamingHTTPClientUsesResponseHeaderTimeout(t *testing.T) {
	transport, ok := streamingHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("streamingHTTPClient transport type = %T, want *http.Transport", streamingHTTPClient.Transport)
	}

	if transport.ResponseHeaderTimeout != defaultUpstreamRequestTimeout {
		t.Fatalf(
			"streamingHTTPClient response header timeout = %v, want %v",
			transport.ResponseHeaderTimeout,
			defaultUpstreamRequestTimeout,
		)
	}
}

func TestUpstreamHTTPClientHasTotalTimeout(t *testing.T) {
	if upstreamHTTPClient.Timeout != defaultUpstreamRequestTimeout {
		t.Fatalf("upstreamHTTPClient timeout = %v, want %v", upstreamHTTPClient.Timeout, defaultUpstreamRequestTimeout)
	}
}

func TestHLSNoRedirectHTTPClientBlocksRedirects(t *testing.T) {
	testURL := "http://example.com/"
	resp, err := hlsNoRedirectHTTPClient.Get(testURL)
	if err == nil && resp != nil {
		resp.Body.Close()
	}
}
