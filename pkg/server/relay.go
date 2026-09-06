package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

const (
	relayPacketSize    = 188
	relayChunkSize     = relayPacketSize * 32
	relayQueueSize     = 256
	relayStartupBudget = 20 * time.Second
	relayWriteTimeout  = 5 * time.Second
)

// relayManager owns session lifetime. Its mutex serializes joins, idle expiry,
// and removal; an entry stays published until its upstream has fully closed.
type relayManager struct {
	mu          sync.Mutex
	sessions    map[string]*relaySession
	closed      bool
	client      *http.Client
	config      config.RelayConfig
	startup     time.Duration
	reconnects  uint64
	slowViewers uint64
}

type relaySession struct {
	manager *relayManager
	key     string
	url     string
	request http.Header
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	ready   chan struct{}

	// Protected by manager.mu.
	viewers  map[*relaySubscription]struct{}
	idle     *time.Timer
	idleSeq  uint64
	started  bool
	stopping bool
	status   int
	header   http.Header
	upstream bool
}

type relaySubscription struct {
	session *relaySession
	chunks  chan []byte
	done    chan struct{}
}

func newRelayManager(cfg config.RelayConfig, client *http.Client) *relayManager {
	return &relayManager{
		sessions: make(map[string]*relaySession),
		client:   client, config: cfg, startup: relayStartupBudget,
	}
}

// subscribe atomically joins or starts one upstream. Waiting for a closing
// generation prevents overlap even when an idle timer races with a new viewer.
func (m *relayManager) subscribe(ctx context.Context, rawURL string, headers http.Header) (*relaySubscription, int) {
	headers = relayRequestHeaders(headers)
	key := relayKey(rawURL, headers)
	for {
		m.mu.Lock()
		if m.closed || ctx.Err() != nil {
			m.mu.Unlock()
			return nil, http.StatusServiceUnavailable
		}
		s := m.sessions[key]
		if s != nil && s.stopping {
			m.mu.Unlock()
			select {
			case <-s.done:
				continue
			case <-ctx.Done():
				return nil, http.StatusServiceUnavailable
			}
		}
		if s == nil {
			sessionCtx, cancel := context.WithCancel(context.Background())
			s = &relaySession{
				manager: m, key: key, url: rawURL, request: headers,
				ctx: sessionCtx, cancel: cancel, done: make(chan struct{}),
				ready: make(chan struct{}), viewers: make(map[*relaySubscription]struct{}),
				status: http.StatusBadGateway,
			}
			m.sessions[key] = s
			go s.run()
		}
		s.idleSeq++
		if s.idle != nil {
			s.idle.Stop()
			s.idle = nil
		}
		sub := &relaySubscription{s, make(chan []byte, relayQueueSize), make(chan struct{})}
		s.viewers[sub] = struct{}{}
		m.mu.Unlock()

		select {
		case <-s.ready:
			m.mu.Lock()
			stopped, status := s.stopping, s.status
			m.mu.Unlock()
			if !stopped && ctx.Err() == nil {
				return sub, http.StatusOK
			}
			sub.release()
			return nil, status
		case <-s.done:
			sub.release()
			return nil, s.status
		case <-ctx.Done():
			sub.release()
			return nil, http.StatusServiceUnavailable
		}
	}
}

func (sub *relaySubscription) release() {
	m := sub.session.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	sub.session.removeLocked(sub)
}

func (s *relaySession) removeLocked(sub *relaySubscription) {
	if _, ok := s.viewers[sub]; !ok {
		return
	}
	delete(s.viewers, sub)
	close(sub.done)
	if len(s.viewers) != 0 || s.stopping {
		return
	}
	if !s.started || s.manager.config.IdleTimeout == 0 {
		s.stopLocked()
		return
	}
	s.idleSeq++
	seq := s.idleSeq
	s.idle = time.AfterFunc(s.manager.config.IdleTimeout, func() {
		s.manager.mu.Lock()
		defer s.manager.mu.Unlock()
		if seq == s.idleSeq && len(s.viewers) == 0 {
			s.stopLocked()
		}
	})
}

func (s *relaySession) stopLocked() {
	if s.stopping {
		return
	}
	s.stopping = true
	s.cancel()
	if s.idle != nil {
		s.idle.Stop()
	}
	for sub := range s.viewers {
		delete(s.viewers, sub)
		close(sub.done)
	}
}

func (m *relayManager) Close() {
	m.mu.Lock()
	m.closed = true
	sessions := make([]*relaySession, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.stopLocked()
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		<-s.done
	}
}

type relayStats struct {
	Sessions        int    `json:"sessions"`
	Viewers         int    `json:"viewers"`
	Upstreams       int    `json:"upstreams"`
	Reconnects      uint64 `json:"reconnects"`
	SlowDisconnects uint64 `json:"slow_disconnects"`
}

func (m *relayManager) stats() relayStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	stats := relayStats{Sessions: len(m.sessions), Reconnects: m.reconnects, SlowDisconnects: m.slowViewers}
	for _, s := range m.sessions {
		stats.Viewers += len(s.viewers)
		if s.upstream {
			stats.Upstreams++
		}
	}
	return stats
}

func (s *relaySession) run() {
	m := s.manager
	startup := time.AfterFunc(m.startup, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !s.started && !s.stopping {
			s.status = http.StatusGatewayTimeout
			s.stopLocked()
		}
	})
	defer func() {
		startup.Stop()
		m.mu.Lock()
		s.stopLocked()
		delete(m.sessions, s.key)
		close(s.done)
		m.mu.Unlock()
	}()

	backoff := m.config.ReconnectInitial
	for s.ctx.Err() == nil {
		delivered, permanent := s.readAttempt()
		if permanent || s.ctx.Err() != nil {
			return
		}
		if delivered {
			backoff = m.config.ReconnectInitial
		}
		m.mu.Lock()
		m.reconnects++
		m.mu.Unlock()
		log.Printf("[iptv-proxy] relay %.12s reconnecting in %s", s.key, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < m.config.ReconnectMax {
			if backoff > m.config.ReconnectMax/2 {
				backoff = m.config.ReconnectMax
			} else {
				backoff *= 2
			}
		}
	}
}

func (s *relaySession) readAttempt() (delivered, permanent bool) {
	m := s.manager
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	// Reset only on received bytes: this is inactivity, not stream duration.
	inactivity := time.AfterFunc(m.config.ReadTimeout, cancel)
	defer inactivity.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return false, true
	}
	req.Header = s.request.Clone()
	resp, err := m.client.Do(req)
	if err != nil {
		return false, false
	}
	m.mu.Lock()
	s.upstream = true
	m.mu.Unlock()
	defer func() {
		resp.Body.Close()
		m.mu.Lock()
		s.upstream = false
		m.mu.Unlock()
	}()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
			resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
			m.mu.Lock()
			s.status = resp.StatusCode
			m.mu.Unlock()
			return false, true
		}
		return false, false
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return false, true
	}
	header := relayResponseHeaders(resp.Header)
	var packets relayPackets // Partial TS bytes never cross an upstream generation.
	buf := make([]byte, relayChunkSize)
	for ctx.Err() == nil {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			inactivity.Reset(m.config.ReadTimeout)
			packets.push(buf[:n], func(chunk []byte) {
				delivered = true
				m.mu.Lock()
				defer m.mu.Unlock()
				if s.stopping {
					return
				}
				if !s.started {
					s.header = header
					s.started = true
					close(s.ready)
				}
				for sub := range s.viewers {
					select {
					case sub.chunks <- chunk:
					default:
						m.slowViewers++
						log.Printf("[iptv-proxy] relay %.12s disconnecting slow viewer", s.key)
						s.removeLocked(sub)
					}
				}
			})
		}
		if err != nil {
			break
		}
	}
	return delivered, false
}

func relayRequestHeaders(src http.Header) http.Header {
	dst := relayEndToEndHeaders(src)
	for _, key := range []string{
		"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since",
		"Forwarded", "X-Forwarded-For", "X-Real-Ip",
	} {
		dst.Del(key)
	}
	dst.Set("Accept-Encoding", "identity")
	return dst
}

func relayResponseHeaders(src http.Header) http.Header {
	dst := relayEndToEndHeaders(src)
	for _, key := range []string{"Content-Length", "Content-Range", "Accept-Ranges", "Etag", "Last-Modified", "Content-Encoding", "Set-Cookie"} {
		dst.Del(key)
	}
	dst.Set("Cache-Control", "no-store")
	return dst
}

func relayEndToEndHeaders(src http.Header) http.Header {
	dst := make(http.Header)
	mergeHttpHeader(dst, src)
	for _, value := range src.Values("Connection") {
		for _, key := range strings.Split(value, ",") {
			dst.Del(strings.TrimSpace(key))
		}
	}
	return dst
}

func relayKey(rawURL string, headers http.Header) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q\n", rawURL)
	keys := make([]string, 0, len(headers))
	for key := range headers {
		if key != "User-Agent" && key != "Accept" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "%q:%q\n", key, headers[key])
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
}

// relayPackets keeps at most a partial packet plus sync lookahead between reads.
// It does not retain delivered packets or parse codecs/PAT/PMT/keyframes.
type relayPackets struct {
	pending []byte
	synced  bool
}

func (p *relayPackets) push(data []byte, emit func([]byte)) {
	p.pending = append(p.pending, data...)
	for len(p.pending) >= relayPacketSize {
		if !p.synced {
			i := 0
			for i+relayPacketSize < len(p.pending) {
				if p.pending[i] == 0x47 && p.pending[i+relayPacketSize] == 0x47 {
					p.synced = true
					break
				}
				i++
			}
			p.pending = p.pending[i:]
			if !p.synced {
				break
			}
		}
		n := 0
		for n+relayPacketSize <= len(p.pending) && n < relayChunkSize && p.pending[n] == 0x47 {
			n += relayPacketSize
		}
		if n > 0 {
			chunk := append([]byte(nil), p.pending[:n]...)
			p.pending = p.pending[n:]
			emit(chunk)
		} else {
			p.synced = false
		}
	}
	// Do not keep an entire read allocation alive for a tiny remainder.
	p.pending = append([]byte(nil), p.pending...)
}
