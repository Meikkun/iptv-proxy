package server

import (
	"net/http"
	"time"
)

const defaultUpstreamRequestTimeout = 30 * time.Second

func newUpstreamHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultUpstreamRequestTimeout}
}

func newStreamingHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = defaultUpstreamRequestTimeout

	return &http.Client{Transport: transport}
}
