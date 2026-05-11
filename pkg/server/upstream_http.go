package server

import (
	"net/http"
	"time"
)

const (
	defaultUpstreamRequestTimeout = 30 * time.Second
	streamingCopyBufSize          = 128 * 1024
)

// streamingHTTPClient is a shared client for long-lived streaming requests.
// It has no overall timeout; only a ResponseHeaderTimeout to detect dead
// upstreams. The underlying transport is safe for concurrent use and
// maintains a connection pool.
var streamingHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = defaultUpstreamRequestTimeout
	return &http.Client{Transport: transport}
}()

// upstreamHTTPClient is a shared client for short-lived upstream requests
// (playlist downloads, API calls). It enforces a 30-second total timeout.
var upstreamHTTPClient = &http.Client{
	Timeout: defaultUpstreamRequestTimeout,
}

// hlsNoRedirectHTTPClient is a shared client for HLS manifest fetches where
// we need to capture 302 redirects instead of following them. It has a total
// timeout because manifest fetches are expected to complete quickly.
var hlsNoRedirectHTTPClient = &http.Client{
	Timeout: defaultUpstreamRequestTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// hlsStreamingHTTPClient is a shared client for HLS streaming fallback (when
// upstream does not redirect). No total timeout, no redirect following.
var hlsStreamingHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = defaultUpstreamRequestTimeout
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}()
