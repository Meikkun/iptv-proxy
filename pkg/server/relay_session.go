package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const relayReadBufferSize = 32 * 1024

type RelaySession struct {
	manager *RelayManager
	key     string
	id      string
	channel string
	host    string
	open    relaySourceOpener

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.Mutex
	signal          chan struct{}
	buffer          relayBuffer
	responseHeader  http.Header
	statusCode      int
	ready           bool
	hasSuccess      bool
	lastError       error
	lastErrorText   string
	subscribers     int
	peakSubscribers int
	upstreamBytes   int64
	reconnectCount  int
	idleTimer       *time.Timer
	createdAt       time.Time
	readyAt         time.Time
	closed          bool
	closeReason     string
}

type relaySessionSummary struct {
	subscribers int
	bufferBytes int
	coverage    time.Duration
}

type RelaySubscription struct {
	session *RelaySession
	nextSeq uint64
	once    sync.Once
}

func newRelaySession(manager *RelayManager, key, sessionID, channel, host string, open relaySourceOpener) *RelaySession {
	ctx, cancel := context.WithCancel(context.Background())

	return &RelaySession{
		manager:        manager,
		key:            key,
		id:             sessionID,
		channel:        channel,
		host:           host,
		open:           open,
		ctx:            ctx,
		cancel:         cancel,
		signal:         make(chan struct{}),
		buffer:         newRelayBuffer(manager.bufferDuration, manager.maxBufferBytes),
		responseHeader: make(http.Header),
		createdAt:      time.Now(),
	}
}

func (s *RelaySession) run() {
	backoff := s.manager.reconnectDelay

	for {
		if err := s.ctx.Err(); err != nil {
			return
		}

		hadData, err := s.streamOnce()
		if err == nil {
			return
		}

		if errors.Is(err, context.Canceled) || (errors.Is(err, io.EOF) && s.ctx.Err() != nil) {
			return
		}

		if hadData {
			backoff = s.manager.reconnectDelay
		}

		s.mu.Lock()
		s.reconnectCount++
		s.mu.Unlock()
		s.manager.recordReconnect()
		s.manager.logf("reconnect session=%s channel=%q upstream_host=%s backoff_ms=%d had_data=%t error=%q", s.id, s.channel, s.host, backoff.Milliseconds(), hadData, sanitizeRelayError(err))

		timer := time.NewTimer(backoff)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		backoff *= 2
		if backoff > s.manager.reconnectMax {
			backoff = s.manager.reconnectMax
		}
	}
}

func (s *RelaySession) streamOnce() (bool, error) {
	resp, err := s.open(s.ctx)
	if err != nil {
		s.recordError(err)
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		err := fmt.Errorf("upstream returned status %d", resp.StatusCode)
		s.recordError(err)
		return false, err
	}

	s.recordReady(resp.StatusCode, resp.Header)

	readBuffer := make([]byte, relayReadBufferSize)
	hadData := false
	for {
		n, readErr := resp.Body.Read(readBuffer)
		if n > 0 {
			hadData = true
			s.appendChunk(readBuffer[:n])
		}

		if readErr == nil {
			continue
		}

		if errors.Is(readErr, io.EOF) {
			if hadData {
				// Clean EOF after receiving data: likely a finite stream.
				// Let the caller decide whether to reconnect or close.
				s.recordError(readErr)
				return hadData, readErr
			}
			readErr = io.ErrUnexpectedEOF
		}

		s.recordError(readErr)
		return hadData, readErr
	}
}

func (s *RelaySession) Subscribe(ctx context.Context) (*relayStart, error) {
	startCtx, cancel := context.WithTimeout(ctx, relayStartupTimeout)
	defer cancel()

	s.mu.Lock()
	if s.closed {
		err := s.lastError
		if err == nil {
			err = io.EOF
		}
		s.mu.Unlock()
		return nil, err
	}
	s.cancelIdleTimerLocked()
	s.subscribers++
	if s.subscribers > s.peakSubscribers {
		s.peakSubscribers = s.subscribers
	}
	s.mu.Unlock()

	subscribed := false
	defer func() {
		if !subscribed {
			s.removeSubscriber()
		}
	}()

	for {
		s.mu.Lock()
		if s.closed {
			err := s.lastError
			if err == nil {
				err = io.EOF
			}
			s.mu.Unlock()
			return nil, err
		}

		if s.ready && s.buffer.hasData() {
			startSeq := s.buffer.startSeqForDelay(s.manager.targetDelay)
			start := &relayStart{
				Header:     cloneHTTPHeader(s.responseHeader),
				StatusCode: s.statusCode,
				Subscription: &RelaySubscription{
					session: s,
					nextSeq: startSeq,
				},
			}
			coverage := s.buffer.coverage()
			subscribers := s.subscribers
			subscribed = true
			s.mu.Unlock()
			s.manager.RecordHit()
			s.manager.logf("subscriber_joined session=%s channel=%q subs=%d coverage_ms=%d target_delay_ms=%d", s.id, s.channel, subscribers, coverage.Milliseconds(), s.manager.targetDelay.Milliseconds())
			return start, nil
		}

		waitCh := s.signal
		lastErr := s.lastError
		hasSuccess := s.hasSuccess
		s.mu.Unlock()

		select {
		case <-startCtx.Done():
			if lastErr != nil && !hasSuccess {
				return nil, lastErr
			}
			return nil, startCtx.Err()
		case <-waitCh:
		}
	}
}

func (s *RelaySession) appendChunk(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}

	s.buffer.append(time.Now(), data)
	s.upstreamBytes += int64(len(data))
	s.broadcastLocked()
}

func (s *RelaySession) recordReady(statusCode int, header http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}

	s.ready = true
	isReconnectRecovery := s.hasSuccess && s.lastError != nil
	s.hasSuccess = true
	s.statusCode = statusCode
	s.responseHeader = cloneHTTPHeader(header)
	s.lastError = nil
	s.lastErrorText = ""
	if s.readyAt.IsZero() {
		s.readyAt = time.Now()
	}
	s.broadcastLocked()

	contentType := header.Get("Content-Type")
	if contentType == "" {
		contentType = "unknown"
	}

	startup := time.Since(s.createdAt)
	if isReconnectRecovery {
		s.manager.logf("reconnect_recovered session=%s channel=%q upstream_host=%s status=%d content_type=%q startup_ms=%d", s.id, s.channel, s.host, statusCode, contentType, startup.Milliseconds())
		return
	}

	s.manager.logf("session_ready session=%s channel=%q upstream_host=%s status=%d content_type=%q startup_ms=%d", s.id, s.channel, s.host, statusCode, contentType, startup.Milliseconds())
}

func (s *RelaySession) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}

	s.lastError = err
	s.lastErrorText = sanitizeRelayError(err)
	s.broadcastLocked()
	s.manager.recordUpstreamFailure()
}

func (s *RelaySession) nextChunk(ctx context.Context, nextSeq uint64) (relayChunk, error) {
	for {
		s.mu.Lock()
		if !s.buffer.seqAvailable(nextSeq) {
			s.mu.Unlock()
			return relayChunk{}, fmt.Errorf("subscriber underrun: requested seq %d has been trimmed from buffer", nextSeq)
		}
		if chunk, ok := s.buffer.chunkAtOrAfter(nextSeq); ok {
			s.mu.Unlock()
			return chunk, nil
		}

		if s.closed {
			err := s.lastError
			if err == nil {
				err = io.EOF
			}
			s.mu.Unlock()
			return relayChunk{}, err
		}

		waitCh := s.signal
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return relayChunk{}, ctx.Err()
		case <-waitCh:
		}
	}
}

func (s *RelaySession) removeSubscriber() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.subscribers > 0 {
		s.subscribers--
	}

	s.manager.logf("subscriber_left session=%s channel=%q subs=%d", s.id, s.channel, s.subscribers)

	if s.subscribers == 0 {
		s.scheduleIdleStopLocked()
	}
}

func (s *RelaySession) cancelIdleTimerLocked() {
	if s.idleTimer == nil {
		return
	}

	s.idleTimer.Stop()
	s.idleTimer = nil
}

func (s *RelaySession) scheduleIdleStopLocked() {
	if s.closed {
		return
	}

	if s.manager.idleTimeout <= 0 {
		go s.manager.removeSession(s.key, s, "idle")
		return
	}

	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}

	s.idleTimer = time.AfterFunc(s.manager.idleTimeout, func() {
		s.manager.removeSession(s.key, s, "idle")
	})
}

func (s *RelaySession) broadcastLocked() {
	if s.signal != nil {
		close(s.signal)
	}
	s.signal = make(chan struct{})
}

func (s *RelaySubscription) NextChunk(ctx context.Context) ([]byte, error) {
	chunk, err := s.session.nextChunk(ctx, s.nextSeq)
	if err != nil {
		return nil, err
	}

	s.nextSeq = chunk.seq + 1
	return chunk.data, nil
}

func (s *RelaySubscription) Close() {
	s.once.Do(func() {
		s.session.removeSubscriber()
	})
}

func (s *RelaySession) close() {
	s.closeWithReason("shutdown")
}

func (s *RelaySession) closeWithReason(reason string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	s.closed = true
	if s.closeReason == "" {
		s.closeReason = reason
	}
	s.cancelIdleTimerLocked()
	if s.signal != nil {
		close(s.signal)
		s.signal = nil
	}

	bufferCoverage := s.buffer.coverage()
	bufferBytes := s.buffer.bytes()
	peakSubscribers := s.peakSubscribers
	reconnectCount := s.reconnectCount
	upstreamBytes := s.upstreamBytes
	lifetime := time.Since(s.createdAt)
	closeReason := s.closeReason
	s.mu.Unlock()

	s.cancel()
	s.manager.logf("session_closed session=%s channel=%q upstream_host=%s reason=%s lifetime_ms=%d reconnects=%d peak_subs=%d upstream_bytes=%d buffered_coverage_ms=%d buffered_bytes=%d", s.id, s.channel, s.host, closeReason, lifetime.Milliseconds(), reconnectCount, peakSubscribers, upstreamBytes, bufferCoverage.Milliseconds(), bufferBytes)
}

func (s *RelaySession) summarySnapshot() relaySessionSummary {
	s.mu.Lock()
	defer s.mu.Unlock()

	return relaySessionSummary{
		subscribers: s.subscribers,
		bufferBytes: s.buffer.bytes(),
		coverage:    s.buffer.coverage(),
	}
}

func sanitizeRelayError(err error) string {
	if err == nil {
		return ""
	}

	text := strings.TrimSpace(err.Error())
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}

	if len(text) > 200 {
		text = text[:200]
	}

	return text
}
