package server

import (
	"net/http"
	"time"
)

const defaultUpstreamRequestTimeout = 30 * time.Second

func newUpstreamHTTPClient() *http.Client {
	return &http.Client{Timeout: defaultUpstreamRequestTimeout}
}
