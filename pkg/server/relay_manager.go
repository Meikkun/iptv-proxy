package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
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

type relayBypassReason string

const (
	relayBypassNone          relayBypassReason = ""
	relayBypassNoTrack       relayBypassReason = "no_track"
	relayBypassRange         relayBypassReason = "range"
	relayBypassHLS           relayBypassReason = "hls"
	relayBypassIneligibleExt relayBypassReason = "ineligible_ext"
)

type relayCounters struct {
	sessionsCreated     uint64
	relayHits           uint64
	reconnects          uint64
	upstreamFailures    uint64
	bypassNoTrack       uint64
	bypassRange         uint64
	bypassHLS           uint64
	bypassIneligibleExt uint64
}

type relaySummarySnapshot struct {
	activeSessions    int
	activeSubscribers int
	bufferedBytes     int
	counters          relayCounters
}

type RelayManager struct {
	bufferDuration  time.Duration
	targetDelay     time.Duration
	idleTimeout     time.Duration
	reconnectDelay  time.Duration
	reconnectMax    time.Duration
	maxBufferBytes  int
	logSummaryEvery time.Duration
	logVerbose      bool

	mu       sync.Mutex
	sessions map[string]*RelaySession

	statsMu sync.Mutex
	stats   relayCounters

	summaryOnce sync.Once
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

	logSummaryEvery := cfg.RelayLogSummaryEvery
	if logSummaryEvery < 0 {
		logSummaryEvery = 0
	}

	return &RelayManager{
		bufferDuration:  bufferDuration,
		targetDelay:     targetDelay,
		idleTimeout:     idleTimeout,
		reconnectDelay:  reconnectDelay,
		reconnectMax:    reconnectMax,
		maxBufferBytes:  maxBufferBytes,
		logSummaryEvery: logSummaryEvery,
		logVerbose:      cfg.RelayLogVerbose,
		sessions:        make(map[string]*RelaySession),
	}
}

func (m *RelayManager) GetOrCreate(rawURL *url.URL, requestHeader http.Header, track *m3u.Track) *RelaySession {
	key := relaySessionKey(rawURL, requestHeader)
	sessionID := relaySessionID(key)
	channel := relayTrackLabel(track, rawURL)
	upstreamHost := rawURL.Hostname()
	if upstreamHost == "" {
		upstreamHost = rawURL.Host
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if session, ok := m.sessions[key]; ok {
		return session
	}

	session := newRelaySession(
		m,
		key,
		sessionID,
		channel,
		upstreamHost,
		newHTTPRelaySourceOpener(rawURL, relaySessionHeaders(requestHeader)),
	)
	m.sessions[key] = session
	m.recordSessionCreated()
	m.logf("session_created session=%s channel=%q upstream_host=%s", sessionID, channel, upstreamHost)
	go session.run()

	return session
}

func (m *RelayManager) removeSession(key string, session *RelaySession, reason string) {
	m.mu.Lock()
	current, ok := m.sessions[key]
	if ok && current == session {
		delete(m.sessions, key)
	}
	m.mu.Unlock()

	session.closeWithReason(reason)
}

func (m *RelayManager) StartSummaryLogging(ctx context.Context) {
	m.summaryOnce.Do(func() {
		m.logStartupConfig()
		if m.logSummaryEvery <= 0 {
			return
		}

		go func() {
			ticker := time.NewTicker(m.logSummaryEvery)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					m.logSummary()
				}
			}
		}()
	})
}

func (m *RelayManager) RecordHit() {
	m.statsMu.Lock()
	m.stats.relayHits++
	m.statsMu.Unlock()
}

func (m *RelayManager) RecordBypass(reason relayBypassReason, track *m3u.Track, requestHeader http.Header) {
	if reason == relayBypassNone {
		return
	}

	m.statsMu.Lock()
	switch reason {
	case relayBypassNoTrack:
		m.stats.bypassNoTrack++
	case relayBypassRange:
		m.stats.bypassRange++
	case relayBypassHLS:
		m.stats.bypassHLS++
	case relayBypassIneligibleExt:
		m.stats.bypassIneligibleExt++
	}
	m.statsMu.Unlock()

	if !m.logVerbose && !config.DebugLoggingEnabled {
		return
	}

	channel := relayTrackLabel(track, nil)
	m.logf("bypass reason=%s channel=%q", reason, channel)
}

func (m *RelayManager) recordReconnect() {
	m.statsMu.Lock()
	m.stats.reconnects++
	m.statsMu.Unlock()
}

func (m *RelayManager) recordUpstreamFailure() {
	m.statsMu.Lock()
	m.stats.upstreamFailures++
	m.statsMu.Unlock()
}

func (m *RelayManager) recordSessionCreated() {
	m.statsMu.Lock()
	m.stats.sessionsCreated++
	m.statsMu.Unlock()
}

func (m *RelayManager) logStartupConfig() {
	m.logf(
		"enabled=true buffer_ms=%d target_delay_ms=%d idle_timeout_ms=%d reconnect_delay_ms=%d reconnect_max_ms=%d max_buffer_mb=%d summary_interval_ms=%d verbose=%t",
		m.bufferDuration.Milliseconds(),
		m.targetDelay.Milliseconds(),
		m.idleTimeout.Milliseconds(),
		m.reconnectDelay.Milliseconds(),
		m.reconnectMax.Milliseconds(),
		m.maxBufferBytes/(1024*1024),
		m.logSummaryEvery.Milliseconds(),
		m.logVerbose,
	)
}

func (m *RelayManager) logSummary() {
	snapshot := m.summarySnapshot()
	m.logf(
		"summary active_sessions=%d active_subscribers=%d sessions_created=%d relay_hits=%d bypass_hls=%d bypass_range=%d bypass_no_track=%d bypass_ineligible_ext=%d reconnects=%d upstream_failures=%d buffered_mb=%d",
		snapshot.activeSessions,
		snapshot.activeSubscribers,
		snapshot.counters.sessionsCreated,
		snapshot.counters.relayHits,
		snapshot.counters.bypassHLS,
		snapshot.counters.bypassRange,
		snapshot.counters.bypassNoTrack,
		snapshot.counters.bypassIneligibleExt,
		snapshot.counters.reconnects,
		snapshot.counters.upstreamFailures,
		snapshot.bufferedBytes/(1024*1024),
	)
}

func (m *RelayManager) summarySnapshot() relaySummarySnapshot {
	m.mu.Lock()
	sessions := make([]*RelaySession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()

	activeSubscribers := 0
	bufferedBytes := 0
	for _, session := range sessions {
		summary := session.summarySnapshot()
		activeSubscribers += summary.subscribers
		bufferedBytes += summary.bufferBytes
	}

	m.statsMu.Lock()
	counters := m.stats
	m.statsMu.Unlock()

	return relaySummarySnapshot{
		activeSessions:    len(sessions),
		activeSubscribers: activeSubscribers,
		bufferedBytes:     bufferedBytes,
		counters:          counters,
	}
}

func (m *RelayManager) logf(format string, args ...interface{}) {
	log.Printf("[relay] "+format, args...)
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

func relaySessionID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

func relayTrackLabel(track *m3u.Track, rawURL *url.URL) string {
	if track != nil {
		if name := sanitizeRelayLabel(track.Name); name != "" {
			return name
		}
	}

	if rawURL != nil {
		if base := sanitizeRelayLabel(strings.Trim(rawURL.Path, "/")); base != "" {
			return base
		}
	}

	return "unknown"
}

func sanitizeRelayLabel(value string) string {
	cleaned := strings.TrimSpace(value)
	cleaned = strings.ReplaceAll(cleaned, "\n", " ")
	cleaned = strings.ReplaceAll(cleaned, "\r", " ")
	cleaned = strings.ReplaceAll(cleaned, "\t", " ")
	for strings.Contains(cleaned, "  ") {
		cleaned = strings.ReplaceAll(cleaned, "  ", " ")
	}

	if len(cleaned) > 80 {
		cleaned = cleaned[:80]
	}

	return cleaned
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

func relayEligibility(track *m3u.Track, requestHeader http.Header) (bool, relayBypassReason) {
	if track == nil {
		return false, relayBypassNoTrack
	}

	if requestHeader.Get("Range") != "" {
		return false, relayBypassRange
	}

	lowerURI := strings.ToLower(track.URI)
	if strings.HasSuffix(lowerURI, ".m3u8") {
		return false, relayBypassHLS
	}

	extIndex := strings.LastIndex(strings.SplitN(strings.SplitN(lowerURI, "?", 2)[0], "#", 2)[0], ".")
	if extIndex == -1 {
		return false, relayBypassIneligibleExt
	}

	ext := lowerURI[extIndex:]
	switch ext {
	case ".ts", ".mpegts", ".mts", ".m2ts":
		return true, relayBypassNone
	default:
		return false, relayBypassIneligibleExt
	}
}

func isRelayEligibleTrack(track *m3u.Track, requestHeader http.Header) bool {
	eligible, _ := relayEligibility(track, requestHeader)
	return eligible
}
