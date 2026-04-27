package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jamesnetherton/m3u"
)

func TestRelaySessionSharesSingleUpstreamWithMultipleSubscribers(t *testing.T) {
	manager := &RelayManager{
		bufferDuration: 10 * time.Second,
		targetDelay:    0,
		idleTimeout:    time.Second,
		reconnectDelay: 10 * time.Millisecond,
		reconnectMax:   10 * time.Millisecond,
		maxBufferBytes: 1024,
		sessions:       make(map[string]*RelaySession),
	}

	var (
		callCount int
		writePipe *io.PipeWriter
		readyCh   = make(chan struct{})
	)

	opener := func(ctx context.Context) (*relayUpstreamResponse, error) {
		callCount++
		if callCount > 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		reader, writer := io.Pipe()
		writePipe = writer
		close(readyCh)

		return &relayUpstreamResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"video/mp2t"}},
			Body:       reader,
		}, nil
	}

	session := newRelaySession(manager, "channel", "session-a", "channel", "provider.example", opener)
	go session.run()
	defer session.close()

	<-readyCh
	if _, err := writePipe.Write([]byte("shared-stream")); err != nil {
		t.Fatalf("writePipe.Write() error = %v", err)
	}

	startOne, err := session.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("session.Subscribe() error = %v", err)
	}
	defer startOne.Subscription.Close()

	startTwo, err := session.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("session.Subscribe() error = %v", err)
	}
	defer startTwo.Subscription.Close()

	chunkOne, err := startOne.Subscription.NextChunk(context.Background())
	if err != nil {
		t.Fatalf("startOne.Subscription.NextChunk() error = %v", err)
	}

	chunkTwo, err := startTwo.Subscription.NextChunk(context.Background())
	if err != nil {
		t.Fatalf("startTwo.Subscription.NextChunk() error = %v", err)
	}

	if string(chunkOne) != "shared-stream" || string(chunkTwo) != "shared-stream" {
		t.Fatalf("chunks = %q, %q, want shared-stream", string(chunkOne), string(chunkTwo))
	}

	if callCount != 1 {
		t.Fatalf("opener call count = %d, want 1", callCount)
	}

	_ = writePipe.Close()
}

func TestRelaySessionReconnectsAndContinuesStreaming(t *testing.T) {
	manager := &RelayManager{
		bufferDuration: 10 * time.Second,
		targetDelay:    0,
		idleTimeout:    time.Second,
		reconnectDelay: 10 * time.Millisecond,
		reconnectMax:   10 * time.Millisecond,
		maxBufferBytes: 1024,
		sessions:       make(map[string]*RelaySession),
	}

	var (
		mu       sync.Mutex
		call     int
		payloads = []string{"first", "second"}
	)

	opener := func(ctx context.Context) (*relayUpstreamResponse, error) {
		mu.Lock()
		defer mu.Unlock()

		if call >= len(payloads) {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		payload := payloads[call]
		call++
		return &relayUpstreamResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"video/mp2t"}},
			Body:       io.NopCloser(strings.NewReader(payload)),
		}, nil
	}

	session := newRelaySession(manager, "channel", "session-b", "channel", "provider.example", opener)
	go session.run()
	defer session.close()

	start, err := session.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("session.Subscribe() error = %v", err)
	}
	defer start.Subscription.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	first, err := start.Subscription.NextChunk(ctx)
	if err != nil {
		t.Fatalf("NextChunk() error = %v", err)
	}

	second, err := start.Subscription.NextChunk(ctx)
	if err != nil {
		t.Fatalf("NextChunk() error = %v", err)
	}

	if string(first) != "first" || string(second) != "second" {
		t.Fatalf("chunks = %q, %q, want first then second", string(first), string(second))
	}
}

func TestRelaySessionIdleTimeoutClosesSession(t *testing.T) {
	manager := &RelayManager{
		bufferDuration: 10 * time.Second,
		targetDelay:    0,
		idleTimeout:    20 * time.Millisecond,
		reconnectDelay: 10 * time.Millisecond,
		reconnectMax:   10 * time.Millisecond,
		maxBufferBytes: 1024,
		sessions:       make(map[string]*RelaySession),
	}

	reader, writer := io.Pipe()
	session := newRelaySession(manager, "idle-channel", "session-c", "idle-channel", "provider.example", func(ctx context.Context) (*relayUpstreamResponse, error) {
		return &relayUpstreamResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"video/mp2t"}},
			Body:       reader,
		}, nil
	})
	manager.sessions[session.key] = session
	go session.run()
	defer writer.Close()

	if _, err := writer.Write([]byte("warmup")); err != nil {
		t.Fatalf("writer.Write() error = %v", err)
	}

	start, err := session.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("session.Subscribe() error = %v", err)
	}

	start.Subscription.Close()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		_, ok := manager.sessions[session.key]
		manager.mu.Unlock()
		if !ok {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("relay session was not removed after idle timeout")
}

func TestIsRelayEligibleTrack(t *testing.T) {
	track := &m3u.Track{URI: "http://provider.example/live/channel.ts"}
	if !isRelayEligibleTrack(track, http.Header{}) {
		t.Fatal("isRelayEligibleTrack() = false, want true")
	}

	hlsTrack := &m3u.Track{URI: "http://provider.example/live/channel.m3u8"}
	if isRelayEligibleTrack(hlsTrack, http.Header{}) {
		t.Fatal("isRelayEligibleTrack() = true for HLS, want false")
	}

	if isRelayEligibleTrack(track, http.Header{"Range": []string{"bytes=0-10"}}) {
		t.Fatal("isRelayEligibleTrack() = true with Range header, want false")
	}
}

func TestRelaySessionSubscriberCatchesUpAfterJoinDelay(t *testing.T) {
	manager := &RelayManager{
		bufferDuration: 10 * time.Second,
		targetDelay:    2 * time.Second,
		idleTimeout:    time.Second,
		reconnectDelay: 10 * time.Millisecond,
		reconnectMax:   10 * time.Millisecond,
		maxBufferBytes: 1024,
		sessions:       make(map[string]*RelaySession),
	}

	reader, writer := io.Pipe()
	session := newRelaySession(manager, "channel", "session-d", "channel", "provider.example", func(ctx context.Context) (*relayUpstreamResponse, error) {
		return &relayUpstreamResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"video/mp2t"}},
			Body:       reader,
		}, nil
	})
	go session.run()
	defer session.close()

	if _, err := writer.Write([]byte("chunk1")); err != nil {
		t.Fatalf("writer.Write() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := writer.Write([]byte("chunk2")); err != nil {
		t.Fatalf("writer.Write() error = %v", err)
	}

	start, err := session.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("session.Subscribe() error = %v", err)
	}
	defer start.Subscription.Close()

	// With a 2s target delay and only ~50ms between chunks, the subscriber
	// should start at the oldest chunk because the delay is larger than coverage.
	first, err := start.Subscription.NextChunk(context.Background())
	if err != nil {
		t.Fatalf("NextChunk() error = %v", err)
	}
	if string(first) != "chunk1" {
		t.Fatalf("first chunk = %q, want chunk1", string(first))
	}

	second, err := start.Subscription.NextChunk(context.Background())
	if err != nil {
		t.Fatalf("NextChunk() error = %v", err)
	}
	if string(second) != "chunk2" {
		t.Fatalf("second chunk = %q, want chunk2", string(second))
	}

	// After consuming all buffered chunks, the next chunk should wait for new data.
	go func() {
		time.Sleep(20 * time.Millisecond)
		writer.Write([]byte("chunk3")) // nolint: errcheck
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	third, err := start.Subscription.NextChunk(ctx)
	if err != nil {
		t.Fatalf("NextChunk() error = %v", err)
	}
	if string(third) != "chunk3" {
		t.Fatalf("third chunk = %q, want chunk3", string(third))
	}

	_ = writer.Close()
}

func TestRelaySessionSlowSubscriberUnderrun(t *testing.T) {
	manager := &RelayManager{
		bufferDuration: 10 * time.Millisecond,
		targetDelay:    0,
		idleTimeout:    time.Second,
		reconnectDelay: 10 * time.Millisecond,
		reconnectMax:   10 * time.Millisecond,
		maxBufferBytes: 1024,
		sessions:       make(map[string]*RelaySession),
	}

	reader, writer := io.Pipe()
	// Continuously write chunks so the buffer keeps trimming old ones.
	stopWrite := make(chan struct{})
	go func() {
		i := 0
		for {
			select {
			case <-stopWrite:
				writer.Close()
				return
			default:
			}
			payload := fmt.Sprintf("chunk%d", i)
			if _, err := writer.Write([]byte(payload)); err != nil {
				return
			}
			i++
			time.Sleep(5 * time.Millisecond)
		}
	}()

	session := newRelaySession(manager, "channel", "session-e", "channel", "provider.example", func(ctx context.Context) (*relayUpstreamResponse, error) {
		return &relayUpstreamResponse{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"video/mp2t"}},
			Body:       reader,
		}, nil
	})
	go session.run()
	defer func() {
		close(stopWrite)
		session.close()
	}()

	// Wait for buffer to have some data.
	time.Sleep(30 * time.Millisecond)

	start, err := session.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("session.Subscribe() error = %v", err)
	}
	defer start.Subscription.Close()

	// Read one chunk.
	_, _ = start.Subscription.NextChunk(context.Background())

	// Wait long enough for the buffer to trim the chunk we just read.
	time.Sleep(50 * time.Millisecond)

	// Try to read the next seq; if it was trimmed we should get an underrun error.
	_, err = start.Subscription.NextChunk(context.Background())
	if err == nil {
		t.Fatal("expected underrun error for trimmed sequence, got nil")
	}
	if !strings.Contains(err.Error(), "underrun") {
		t.Fatalf("expected underrun in error, got %q", err.Error())
	}
}
