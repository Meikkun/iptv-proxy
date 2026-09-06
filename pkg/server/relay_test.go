package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func testRelayConfig() config.RelayConfig {
	c := config.DefaultRelayConfig()
	c.IdleTimeout = 100 * time.Millisecond
	c.ReconnectInitial = 5 * time.Millisecond
	c.ReconnectMax = 20 * time.Millisecond
	c.ReadTimeout = 2 * time.Second
	return c
}

func testRelayManager(t *testing.T, c config.RelayConfig) *relayManager {
	t.Helper()
	m := newRelayManager(c, streamingHTTPClient)
	t.Cleanup(m.Close)
	return m
}

func tsPackets(value byte, count int) []byte {
	b := bytes.Repeat([]byte{value}, count*relayPacketSize)
	for i := 0; i < len(b); i += relayPacketSize {
		b[i] = 0x47
	}
	return b
}

type relayFixture struct {
	server *httptest.Server
	opened chan chan []byte
	calls  int32
}

func newRelayFixture(t *testing.T) *relayFixture {
	t.Helper()
	f := &relayFixture{opened: make(chan chan []byte, 100)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.calls, 1)
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(tsPackets(0, 2))
		w.(http.Flusher).Flush()
		feed := make(chan []byte)
		f.opened <- feed
		for {
			select {
			case <-r.Context().Done():
				return
			case b := <-feed:
				if b == nil {
					return
				}
				if _, err := w.Write(b); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func receiveChunk(t *testing.T, s *relaySubscription) []byte {
	t.Helper()
	select {
	case chunk := <-s.chunks:
		return chunk
	case <-s.done:
		t.Fatal("subscription unexpectedly disconnected")
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live data")
	}
	return nil
}

func subscribeRelay(t *testing.T, m *relayManager, rawURL string, headers http.Header) *relaySubscription {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, status := m.subscribe(ctx, rawURL, headers)
	if s == nil || status != http.StatusOK {
		t.Fatalf("subscribe returned %v, status %d", s, status)
	}
	t.Cleanup(s.release)
	return s
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func sendFeed(t *testing.T, feed chan []byte, data []byte) {
	t.Helper()
	select {
	case feed <- data:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not accept data")
	}
}

func openedFeed(t *testing.T, f *relayFixture) chan []byte {
	t.Helper()
	select {
	case feed := <-f.opened:
		return feed
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not open")
	}
	return nil
}

func TestRelaySharedLiveEdgeAndIdleReuse(t *testing.T) {
	f := newRelayFixture(t)
	m := testRelayManager(t, testRelayConfig())
	first := subscribeRelay(t, m, f.server.URL, nil)
	receiveChunk(t, first)
	feed := openedFeed(t, f)
	sendFeed(t, feed, tsPackets(1, 2))
	receiveChunk(t, first)

	second := subscribeRelay(t, m, f.server.URL, nil)
	select {
	case <-second.chunks:
		t.Fatal("late viewer received historical data")
	default:
	}
	sendFeed(t, feed, tsPackets(2, 2))
	if !bytes.Equal(receiveChunk(t, first), receiveChunk(t, second)) {
		t.Fatal("viewers did not receive identical live bytes")
	}
	first.release()
	sendFeed(t, feed, tsPackets(3, 2))
	if got := receiveChunk(t, second); got[1] != 3 {
		t.Fatal("disconnect interrupted another viewer")
	}
	second.release()
	third := subscribeRelay(t, m, f.server.URL, nil)
	sendFeed(t, feed, tsPackets(4, 2))
	if got := receiveChunk(t, third); got[1] != 4 {
		t.Fatal("idle reuse replayed bytes")
	}
	if calls := atomic.LoadInt32(&f.calls); calls != 1 {
		t.Fatalf("opened %d upstreams, want one", calls)
	}
	third.release()
	eventually(t, func() bool { return m.stats().Sessions == 0 })
	fourth := subscribeRelay(t, m, f.server.URL, nil)
	receiveChunk(t, fourth)
	if calls := atomic.LoadInt32(&f.calls); calls != 2 {
		t.Fatalf("expiry opened %d total upstreams, want two", calls)
	}
}

func TestRelayConcurrentJoinsAndIdleTimer(t *testing.T) {
	f := newRelayFixture(t)
	cfg := testRelayConfig()
	cfg.IdleTimeout = 3 * time.Millisecond
	m := testRelayManager(t, cfg)
	var clientOverlap int32
	m.client = countingRelayClient(&clientOverlap)
	const viewers = 30
	results := make(chan *relaySubscription, viewers)
	var wg sync.WaitGroup
	for i := 0; i < viewers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, _ := m.subscribe(context.Background(), f.server.URL, nil)
			results <- s
		}()
	}
	wg.Wait()
	if atomic.LoadInt32(&f.calls) != 1 || m.stats().Viewers != viewers {
		t.Fatalf("joins not shared: %+v", m.stats())
	}
	for i := 0; i < viewers; i++ {
		s := <-results
		if s == nil {
			t.Fatal("join failed")
		}
		s.release()
	}
	for i := 0; i < 30; i++ {
		time.Sleep(time.Duration(i%4) * time.Millisecond)
		s := subscribeRelay(t, m, f.server.URL, nil)
		time.Sleep(4 * time.Millisecond)
		select {
		case <-s.done:
			t.Fatal("old idle timer stopped a joined viewer")
		default:
		}
		s.release()
	}
	if atomic.LoadInt32(&clientOverlap) != 0 {
		t.Fatal("replacement overlapped an unclosed upstream body/request")
	}
}

func TestRelayReconnectEOFAndStall(t *testing.T) {
	for _, stall := range []bool{false, true} {
		t.Run(map[bool]string{false: "EOF", true: "inactivity"}[stall], func(t *testing.T) {
			f := newRelayFixture(t)
			cfg := testRelayConfig()
			cfg.ReadTimeout = 40 * time.Millisecond
			m := testRelayManager(t, cfg)
			var clientOverlap int32
			m.client = countingRelayClient(&clientOverlap)
			s := subscribeRelay(t, m, f.server.URL, nil)
			receiveChunk(t, s)
			feed := openedFeed(t, f)
			// Leave a partial packet in the old generation.
			sendFeed(t, feed, tsPackets(9, 1)[:40])
			if !stall {
				sendFeed(t, feed, nil)
			}
			chunk := receiveChunk(t, s)
			if !bytes.Equal(chunk, tsPackets(0, 2)) {
				t.Fatal("reconnect mixed an old partial packet into the new stream")
			}
			if atomic.LoadInt32(&f.calls) != 2 || atomic.LoadInt32(&clientOverlap) != 0 {
				t.Fatalf("reconnect counts: calls=%d overlap=%d", atomic.LoadInt32(&f.calls), atomic.LoadInt32(&clientOverlap))
			}
		})
	}
}

func TestRelayReadTimeoutIsInactivity(t *testing.T) {
	f := newRelayFixture(t)
	cfg := testRelayConfig()
	cfg.ReadTimeout = 100 * time.Millisecond
	m := testRelayManager(t, cfg)
	s := subscribeRelay(t, m, f.server.URL, nil)
	receiveChunk(t, s)
	feed := openedFeed(t, f)
	for i := 0; i < 8; i++ {
		time.Sleep(20 * time.Millisecond)
		sendFeed(t, feed, tsPackets(1, 2))
		receiveChunk(t, s)
	}
	if atomic.LoadInt32(&f.calls) != 1 {
		t.Fatal("healthy stream was limited by total duration")
	}
}

func TestRelaySlowViewerIsolatedAndBounded(t *testing.T) {
	f := newRelayFixture(t)
	m := testRelayManager(t, testRelayConfig())
	fast := subscribeRelay(t, m, f.server.URL, nil)
	receiveChunk(t, fast)
	feed := openedFeed(t, f)
	slow := subscribeRelay(t, m, f.server.URL, nil)
	for i := 0; i < relayQueueSize+2; i++ {
		sendFeed(t, feed, tsPackets(byte(i), 32))
		if chunk := receiveChunk(t, fast); len(chunk) > relayChunkSize {
			t.Fatal("oversize transport chunk")
		}
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("slow viewer was not explicitly disconnected")
	}
	if len(slow.chunks) != relayQueueSize || cap(slow.chunks) != relayQueueSize {
		t.Fatal("slow viewer queue exceeded its fixed bound")
	}
	if m.stats().Viewers != 1 || m.stats().SlowDisconnects != 1 {
		t.Fatalf("unexpected stats: %+v", m.stats())
	}
}

func TestRelayStartupFailuresAndCleanup(t *testing.T) {
	for _, status := range []int{401, 404, 500, 0, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				if status != 0 {
					w.WriteHeader(status)
					w.(http.Flusher).Flush()
				}
				if status == 0 || status == 200 {
					<-r.Context().Done()
				}
			}))
			t.Cleanup(upstream.Close)
			cfg := testRelayConfig()
			cfg.ReadTimeout = 30 * time.Millisecond
			m := testRelayManager(t, cfg)
			m.startup = 90 * time.Millisecond
			s, got := m.subscribe(context.Background(), upstream.URL, nil)
			want := http.StatusGatewayTimeout
			if status == 401 || status == 404 {
				want = status
				if atomic.LoadInt32(&calls) != 1 {
					t.Fatal("permanent failure was retried")
				}
			}
			if s != nil || got != want {
				t.Fatalf("subscribe status=%d, want=%d", got, want)
			}
			eventually(t, func() bool { return m.stats().Sessions == 0 })
		})
	}
}

func TestRelayCancelWaitingViewerAndShutdown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)
	m := testRelayManager(t, testRelayConfig())
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		m.subscribe(ctx, upstream.URL, nil)
	}()
	eventually(t, func() bool { return m.stats().Viewers == 1 })
	cancel()
	<-returned
	eventually(t, func() bool { return m.stats().Sessions == 0 })

	f := newRelayFixture(t)
	// The fixture must close after its manager.
	t.Cleanup(m.Close)
	s := subscribeRelay(t, m, f.server.URL, nil)
	m.Close()
	select {
	case <-s.done:
	default:
		t.Fatal("shutdown left a viewer running")
	}
	if m.stats().Sessions != 0 {
		t.Fatal("shutdown left an upstream running")
	}
	if s, status := m.subscribe(context.Background(), f.server.URL, nil); s != nil || status != 503 {
		t.Fatal("closed manager accepted a new viewer")
	}
}

func TestRelayIdleExpiryCancelsStallAndBackoff(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "stall", true: "backoff"}[fail], func(t *testing.T) {
			var calls int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.Write(tsPackets(0, 2))
				w.(http.Flusher).Flush()
				if !fail {
					<-r.Context().Done()
				}
			}))
			t.Cleanup(upstream.Close)
			cfg := testRelayConfig()
			cfg.IdleTimeout = 10 * time.Millisecond
			cfg.ReconnectInitial = time.Second
			cfg.ReconnectMax = time.Second
			m := testRelayManager(t, cfg)
			s := subscribeRelay(t, m, upstream.URL, nil)
			s.release()
			eventually(t, func() bool { return m.stats().Sessions == 0 })
			if atomic.LoadInt32(&calls) != 1 {
				t.Fatal("idle session reconnected after expiry")
			}
		})
	}
}

func TestRelayCancelledStartupViewerDoesNotOwnSession(t *testing.T) {
	allowData := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-allowData:
		case <-r.Context().Done():
			return
		}
		w.Write(tsPackets(1, 2))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)
	m := testRelayManager(t, testRelayConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		m.subscribe(ctx, upstream.URL, nil)
	}()
	eventually(t, func() bool { return m.stats().Viewers == 1 })
	secondDone := make(chan *relaySubscription, 1)
	go func() {
		sub, _ := m.subscribe(context.Background(), upstream.URL, nil)
		secondDone <- sub
	}()
	eventually(t, func() bool { return m.stats().Viewers == 2 })
	cancel()
	<-firstDone
	close(allowData)
	select {
	case sub := <-secondDone:
		if sub == nil {
			t.Fatal("first viewer cancellation killed the second startup waiter")
		}
		defer sub.release()
		receiveChunk(t, sub)
	case <-time.After(time.Second):
		t.Fatal("second viewer did not start")
	}
}

func TestRelayKeyAndForwardedHeaders(t *testing.T) {
	base := relayRequestHeaders(http.Header{"Authorization": {"Bearer a"}, "Cookie": {"a=1"}})
	url := "http://provider/user/password/1.ts?token=secret"
	key := relayKey(url, base)
	equivalent := base.Clone()
	equivalent.Set("User-Agent", "a different player")
	equivalent.Set("Accept", "*/*")
	equivalent.Set("Range", "bytes=0-")
	equivalent.Set("If-Range", "old-etag")
	equivalent.Set("If-None-Match", "old-etag")
	equivalent.Set("X-Forwarded-For", "192.0.2.1")
	equivalent.Set("Forwarded", "for=192.0.2.1")
	equivalent.Set("X-Real-IP", "192.0.2.1")
	equivalent = relayRequestHeaders(equivalent)
	if key != relayKey(url, equivalent) {
		t.Fatal("player or byte-zero range prevented sharing")
	}

	for _, header := range []string{"Authorization", "Cookie", "Referer", "Origin", "X-Api-Key"} {
		different := base.Clone()
		different.Set(header, "different")
		if key == relayKey(url, different) {
			t.Fatalf("%s failed to isolate credentials/request context", header)
		}
	}
	if key == relayKey(url+"2", base) {
		t.Fatal("different channel/token was shared")
	}
	h := relayRequestHeaders(http.Header{
		"Range": {"bytes=0-"}, "If-Modified-Since": {"yesterday"},
		"Connection": {"X-Hop"}, "X-Hop": {"private"}, "User-Agent": {"player"},
	})
	if h.Get("Range") != "" || h.Get("If-Modified-Since") != "" || h.Get("X-Hop") != "" ||
		h.Get("Accept-Encoding") != "identity" || h.Get("User-Agent") != "player" {
		t.Fatalf("unexpected forwarded headers: %v", h)
	}
}

func TestRelayPacketsJoinAndFragmentation(t *testing.T) {
	original := tsPackets(1, 70)
	input := append([]byte{1, 2, 3, 4, 5}, original...)
	for _, readSize := range []int{1, 187, 188, 189, relayChunkSize} {
		var p relayPackets
		var got []byte
		for i := 0; i < len(input); i += readSize {
			end := i + readSize
			if end > len(input) {
				end = len(input)
			}
			p.push(input[i:end], func(b []byte) {
				if len(b)%188 != 0 || b[0] != 0x47 || len(b) > relayChunkSize {
					t.Fatal("unaligned/oversized output")
				}
				got = append(got, b...)
			})
			if len(p.pending) > relayPacketSize {
				t.Fatal("retained more than sync lookahead")
			}
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("readSize %d corrupted packet data", readSize)
		}
	}
}

type relayRoundTripper func(*http.Request) (*http.Response, error)

func (f relayRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type countedRelayBody struct {
	io.ReadCloser
	once   sync.Once
	active *int32
}

func (b *countedRelayBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { atomic.AddInt32(b.active, -1) })
	return err
}

// Measure the invariant at the HTTP client: a remote handler's cancellation
// goroutine can run later than Body.Close, even after the socket has closed.
func countingRelayClient(overlap *int32) *http.Client {
	var active int32
	return &http.Client{Transport: relayRoundTripper(func(r *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&active, 1) > 1 {
			atomic.StoreInt32(overlap, 1)
		}
		resp, err := streamingHTTPClient.Transport.RoundTrip(r)
		if err != nil {
			atomic.AddInt32(&active, -1)
			return nil, err
		}
		resp.Body = &countedRelayBody{ReadCloser: resp.Body, active: &active}
		return resp, nil
	})}
}

type closingRelayBody struct {
	io.Reader
	ctx     context.Context
	closing chan struct{}
	unblock chan struct{}
}

func (b *closingRelayBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if n != 0 {
		return n, nil
	}
	<-b.ctx.Done()
	return 0, err
}

func (b *closingRelayBody) Close() error {
	close(b.closing)
	<-b.unblock
	return nil
}

func TestRelayReplacementWaitsForBodyClose(t *testing.T) {
	closing, unblock := make(chan struct{}), make(chan struct{})
	var calls int32
	cfg := testRelayConfig()
	cfg.IdleTimeout = 0
	m := testRelayManager(t, cfg)
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(unblock) }) })
	m.client = &http.Client{Transport: relayRoundTripper(func(r *http.Request) (*http.Response, error) {
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &closingRelayBody{
				Reader: bytes.NewReader(tsPackets(1, 2)), ctx: r.Context(), closing: closing, unblock: unblock,
			}}, nil
		}
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}
	s := subscribeRelay(t, m, "http://provider/1.ts", nil)
	s.release()
	<-closing
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.subscribe(context.Background(), "http://provider/1.ts", nil)
	}()
	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatal("replacement opened before old body closed")
	}
	once.Do(func() { close(unblock) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("replacement failed to recover")
	}
}
