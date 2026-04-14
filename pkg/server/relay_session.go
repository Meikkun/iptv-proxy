package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const relayReadBufferSize = 32 * 1024

type RelaySession struct {
	manager *RelayManager
	key     string
	open    relaySourceOpener

	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.Mutex
	signal         chan struct{}
	buffer         relayBuffer
	responseHeader http.Header
	statusCode     int
	ready          bool
	hasSuccess     bool
	lastError      error
	subscribers    int
	idleTimer      *time.Timer
	closed         bool
}

type RelaySubscription struct {
	session *RelaySession
	nextSeq uint64
	once    sync.Once
}

func newRelaySession(manager *RelayManager, key string, open relaySourceOpener) *RelaySession {
	ctx, cancel := context.WithCancel(context.Background())

	return &RelaySession{
		manager:        manager,
		key:            key,
		open:           open,
		ctx:            ctx,
		cancel:         cancel,
		signal:         make(chan struct{}),
		buffer:         newRelayBuffer(manager.bufferDuration, manager.maxBufferBytes),
		responseHeader: make(http.Header),
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

		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) && s.ctx.Err() != nil {
			return
		}

		if hadData {
			backoff = s.manager.reconnectDelay
		}

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
			start := &relayStart{
				Header:     cloneHTTPHeader(s.responseHeader),
				StatusCode: s.statusCode,
				Subscription: &RelaySubscription{
					session: s,
					nextSeq: s.buffer.startSeqForDelay(s.manager.targetDelay),
				},
			}
			subscribed = true
			s.mu.Unlock()
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

func (s *RelaySession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}

	s.closed = true
	s.cancelIdleTimerLocked()
	if s.signal != nil {
		close(s.signal)
		s.signal = nil
	}
	s.mu.Unlock()

	s.cancel()
}

func (s *RelaySession) appendChunk(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}

	s.buffer.append(time.Now(), data)
	s.broadcastLocked()
}

func (s *RelaySession) recordReady(statusCode int, header http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}

	s.ready = true
	s.hasSuccess = true
	s.statusCode = statusCode
	s.responseHeader = cloneHTTPHeader(header)
	s.lastError = nil
	s.broadcastLocked()
}

func (s *RelaySession) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}

	s.lastError = err
	s.broadcastLocked()
}

func (s *RelaySession) nextChunk(ctx context.Context, nextSeq uint64) (relayChunk, error) {
	for {
		s.mu.Lock()
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
		go s.manager.removeSession(s.key, s)
		return
	}

	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}

	s.idleTimer = time.AfterFunc(s.manager.idleTimeout, func() {
		s.manager.removeSession(s.key, s)
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
