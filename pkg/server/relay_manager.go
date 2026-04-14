package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

const relayStartupTimeout = defaultUpstreamRequestTimeout

var relayHeaderAllowlist = []string{
	"Accept",
	"Accept-Language",
	"Authorization",
	"Cookie",
	"Origin",
	"Referer",
	"User-Agent",
}

type relayUpstreamResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

type relaySourceOpener func(ctx context.Context) (*relayUpstreamResponse, error)

type RelayManager struct {
	bufferDuration time.Duration
	targetDelay    time.Duration
	idleTimeout    time.Duration
	reconnectDelay time.Duration
	reconnectMax   time.Duration
	maxBufferBytes int

	mu       sync.Mutex
	sessions map[string]*RelaySession
}

type relayStart struct {
	Header       http.Header
	StatusCode   int
	Subscription *RelaySubscription
}

func NewRelayManager(cfg *config.ProxyConfig) *RelayManager {
	bufferDuration := cfg.RelayBufferDuration
	if bufferDuration <= 0 {
		bufferDuration = 10 * time.Second
	}

	targetDelay := cfg.RelayTargetDelay
	if targetDelay < 0 {
		targetDelay = 0
	}
	if targetDelay > bufferDuration {
		targetDelay = bufferDuration
	}

	idleTimeout := cfg.RelayIdleTimeout
	if idleTimeout < 0 {
		idleTimeout = 0
	}

	reconnectDelay := cfg.RelayReconnectDelay
	if reconnectDelay <= 0 {
		reconnectDelay = 250 * time.Millisecond
	}

	reconnectMax := cfg.RelayReconnectMax
	if reconnectMax < reconnectDelay {
		reconnectMax = reconnectDelay
	}

	maxBufferBytes := cfg.RelayMaxBufferBytes
	if maxBufferBytes <= 0 {
		maxBufferBytes = 32 * 1024 * 1024
	}

	return &RelayManager{
		bufferDuration: bufferDuration,
		targetDelay:    targetDelay,
		idleTimeout:    idleTimeout,
		reconnectDelay: reconnectDelay,
		reconnectMax:   reconnectMax,
		maxBufferBytes: maxBufferBytes,
		sessions:       make(map[string]*RelaySession),
	}
}

func (m *RelayManager) GetOrCreate(rawURL *url.URL, requestHeader http.Header) *RelaySession {
	key := relaySessionKey(rawURL, requestHeader)

	m.mu.Lock()
	defer m.mu.Unlock()

	if session, ok := m.sessions[key]; ok {
		return session
	}

	session := newRelaySession(
		m,
		key,
		newHTTPRelaySourceOpener(rawURL, relaySessionHeaders(requestHeader)),
	)
	m.sessions[key] = session
	go session.run()

	return session
}

func (m *RelayManager) removeSession(key string, session *RelaySession) {
	m.mu.Lock()
	current, ok := m.sessions[key]
	if ok && current == session {
		delete(m.sessions, key)
	}
	m.mu.Unlock()

	session.close()
}

func relaySessionKey(rawURL *url.URL, requestHeader http.Header) string {
	return fmt.Sprintf("%s|%s", rawURL.String(), relayHeaderFingerprint(requestHeader))
}

func relaySessionHeaders(requestHeader http.Header) http.Header {
	headers := make(http.Header)
	for _, name := range relayHeaderAllowlist {
		values := requestHeader.Values(name)
		if len(values) == 0 {
			continue
		}

		for _, value := range values {
			headers.Add(name, value)
		}
	}

	return headers
}

func relayHeaderFingerprint(requestHeader http.Header) string {
	parts := make([]string, 0, len(relayHeaderAllowlist))
	for _, name := range relayHeaderAllowlist {
		values := requestHeader.Values(name)
		if len(values) == 0 {
			continue
		}

		parts = append(parts, fmt.Sprintf("%s=%s", strings.ToLower(name), strings.Join(values, ",")))
	}

	return strings.Join(parts, "&")
}

func newHTTPRelaySourceOpener(rawURL *url.URL, headers http.Header) relaySourceOpener {
	clonedURL := *rawURL
	clonedHeader := headers.Clone()

	return func(ctx context.Context) (*relayUpstreamResponse, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, clonedURL.String(), nil)
		if err != nil {
			return nil, err
		}

		mergeHttpHeader(req.Header, clonedHeader)

		resp, err := newStreamingHTTPClient().Do(req)
		if err != nil {
			return nil, err
		}

		return &relayUpstreamResponse{
			StatusCode: resp.StatusCode,
			Header:     resp.Header.Clone(),
			Body:       resp.Body,
		}, nil
	}
}

func cloneHTTPHeader(header http.Header) http.Header {
	if header == nil {
		return make(http.Header)
	}

	return header.Clone()
}

func isRelayEligibleTrack(track *m3u.Track, requestHeader http.Header) bool {
	if track == nil {
		return false
	}

	if requestHeader.Get("Range") != "" {
		return false
	}

	lowerURI := strings.ToLower(track.URI)
	if strings.HasSuffix(lowerURI, ".m3u8") {
		return false
	}

	extIndex := strings.LastIndex(strings.SplitN(strings.SplitN(lowerURI, "?", 2)[0], "#", 2)[0], ".")
	if extIndex == -1 {
		return false
	}

	ext := lowerURI[extIndex:]
	switch ext {
	case ".ts", ".mpegts", ".mts", ".m2ts":
		return true
	default:
		return false
	}
}
