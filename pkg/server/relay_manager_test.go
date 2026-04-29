package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestRelayRequestRangeMode(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		want    relayRangeMode
	}{
		{name: "no range", headers: http.Header{}, want: relayRangeModeNone},
		{name: "open ended zero", headers: http.Header{"Range": []string{"bytes=0-"}}, want: relayRangeModeOpenEndedZero},
		{name: "open ended zero trimmed", headers: http.Header{"Range": []string{"  bytes=0-  "}}, want: relayRangeModeOpenEndedZero},
		{name: "non zero start", headers: http.Header{"Range": []string{"bytes=100-"}}, want: relayRangeModeOther},
		{name: "bounded range", headers: http.Header{"Range": []string{"bytes=0-100"}}, want: relayRangeModeOther},
		{name: "multi range", headers: http.Header{"Range": []string{"bytes=0-,100-200"}}, want: relayRangeModeOther},
		{name: "malformed", headers: http.Header{"Range": []string{"not-a-range"}}, want: relayRangeModeOther},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := relayRequestRangeMode(tc.headers); got != tc.want {
				t.Fatalf("relayRequestRangeMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRelayEligibilityReasons(t *testing.T) {
	tests := []struct {
		name       string
		track      *m3u.Track
		headers    http.Header
		wantOK     bool
		wantReason relayBypassReason
	}{
		{name: "no track", track: nil, headers: http.Header{}, wantOK: false, wantReason: relayBypassNoTrack},
		{name: "open ended zero range", track: &m3u.Track{URI: "http://provider/channel.ts"}, headers: http.Header{"Range": []string{"bytes=0-"}}, wantOK: true, wantReason: relayBypassNone},
		{name: "range request", track: &m3u.Track{URI: "http://provider/channel.ts"}, headers: http.Header{"Range": []string{"bytes=0-1"}}, wantOK: false, wantReason: relayBypassRange},
		{name: "malformed range", track: &m3u.Track{URI: "http://provider/channel.ts"}, headers: http.Header{"Range": []string{"wat"}}, wantOK: false, wantReason: relayBypassRange},
		{name: "hls", track: &m3u.Track{URI: "http://provider/channel.m3u8"}, headers: http.Header{}, wantOK: false, wantReason: relayBypassHLS},
		{name: "ineligible ext", track: &m3u.Track{URI: "http://provider/channel"}, headers: http.Header{}, wantOK: false, wantReason: relayBypassIneligibleExt},
		{name: "eligible ts", track: &m3u.Track{URI: "http://provider/channel.ts"}, headers: http.Header{}, wantOK: true, wantReason: relayBypassNone},
		{name: "vod positive duration", track: &m3u.Track{URI: "http://provider/movie.ts", Length: 3600}, headers: http.Header{}, wantOK: false, wantReason: relayBypassVOD},
		{name: "ts with query string", track: &m3u.Track{URI: "http://provider/channel.ts?token=abc"}, headers: http.Header{}, wantOK: true, wantReason: relayBypassNone},
		{name: "ts with query and fragment", track: &m3u.Track{URI: "http://provider/channel.ts?token=abc#start"}, headers: http.Header{}, wantOK: true, wantReason: relayBypassNone},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := relayEligibility(tc.track, tc.headers)
			if ok != tc.wantOK {
				t.Fatalf("relayEligibility() ok=%v, want %v", ok, tc.wantOK)
			}
			if reason != tc.wantReason {
				t.Fatalf("relayEligibility() reason=%q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestRelaySessionHeadersOmitRangeHeaders(t *testing.T) {
	headers := relaySessionHeaders(http.Header{
		"Authorization": []string{"Bearer token"},
		"Cookie":        []string{"session=abc"},
		"Range":         []string{"bytes=0-"},
		"If-Range":      []string{"etag"},
	})

	if got := headers.Values("Authorization"); len(got) != 1 || got[0] != "Bearer token" {
		t.Fatalf("Authorization headers = %v, want Bearer token", got)
	}
	if got := headers.Values("Cookie"); len(got) != 1 || got[0] != "session=abc" {
		t.Fatalf("Cookie headers = %v, want session=abc", got)
	}
	if got := headers.Get("Range"); got != "" {
		t.Fatalf("Range header = %q, want empty", got)
	}
	if got := headers.Get("If-Range"); got != "" {
		t.Fatalf("If-Range header = %q, want empty", got)
	}
}

func TestRelaySessionIDIsStableAndRedacted(t *testing.T) {
	key := "http://provider.example/live/user/pass/12345.ts|authorization=secret"
	idOne := relaySessionID(key)
	idTwo := relaySessionID(key)

	if idOne != idTwo {
		t.Fatalf("relaySessionID() not stable: %q != %q", idOne, idTwo)
	}

	if len(idOne) != 12 {
		t.Fatalf("relaySessionID() len=%d, want 12", len(idOne))
	}

	if idOne == key {
		t.Fatalf("relaySessionID() should be redacted hash, got raw key")
	}
}

func TestRelaySessionKeyIgnoresNonAuthHeaders(t *testing.T) {
	urlA, _ := url.Parse("http://provider.example/live/channel.ts")
	urlB, _ := url.Parse("http://provider.example/live/channel.ts")

	authHeaders := http.Header{"Authorization": []string{"Bearer token"}}
	extraHeaders := http.Header{
		"Authorization":   []string{"Bearer token"},
		"User-Agent":      []string{"PlayerA"},
		"Accept-Language": []string{"en-US"},
		"Referer":         []string{"http://example.com"},
	}

	keyA := relaySessionKey(urlA, authHeaders)
	keyB := relaySessionKey(urlB, extraHeaders)

	if keyA != keyB {
		t.Fatalf("relaySessionKey should ignore non-auth headers for sharing: %q != %q", keyA, keyB)
	}
}

func TestRelaySummarySnapshotIncludesCountersAndSessions(t *testing.T) {
	m := NewRelayManager(&config.ProxyConfig{RelayLogSummaryEvery: 0})

	m.RecordBypass(relayBypassNoTrack, nil, http.Header{})
	m.RecordBypass(relayBypassRange, &m3u.Track{URI: "http://provider/channel.ts"}, http.Header{"Range": []string{"bytes=0-10"}})
	m.RecordBypass(relayBypassHLS, &m3u.Track{URI: "http://provider/channel.m3u8"}, http.Header{})
	m.RecordBypass(relayBypassIneligibleExt, &m3u.Track{URI: "http://provider/channel"}, http.Header{})
	m.RecordHit()
	m.recordReconnect()
	m.recordUpstreamFailure()
	m.recordSessionCreated()

	session := newRelaySession(
		m,
		"session-key",
		"abc123def456",
		"Channel One",
		"provider.example",
		func(ctx context.Context) (*relayUpstreamResponse, error) {
			return nil, context.Canceled
		},
	)

	session.mu.Lock()
	session.subscribers = 2
	session.buffer.append(time.Now(), []byte("abcd"))
	session.mu.Unlock()

	m.mu.Lock()
	m.sessions["session-key"] = session
	m.mu.Unlock()

	snapshot := m.summarySnapshot()

	if snapshot.activeSessions != 1 {
		t.Fatalf("summarySnapshot().activeSessions=%d, want 1", snapshot.activeSessions)
	}

	if snapshot.activeSubscribers != 2 {
		t.Fatalf("summarySnapshot().activeSubscribers=%d, want 2", snapshot.activeSubscribers)
	}

	if snapshot.bufferedBytes != 4 {
		t.Fatalf("summarySnapshot().bufferedBytes=%d, want 4", snapshot.bufferedBytes)
	}

	if snapshot.counters.sessionsCreated != 1 ||
		snapshot.counters.relayHits != 1 ||
		snapshot.counters.reconnects != 1 ||
		snapshot.counters.upstreamFailures != 1 ||
		snapshot.counters.bypassNoTrack != 1 ||
		snapshot.counters.bypassRange != 1 ||
		snapshot.counters.bypassHLS != 1 ||
		snapshot.counters.bypassIneligibleExt != 1 {
		t.Fatalf("summarySnapshot().counters unexpected: %+v", snapshot.counters)
	}
}

func TestRelayGetOrCreateSharesSessionForNoRangeAndBytesZero(t *testing.T) {
	manager := NewRelayManager(&config.ProxyConfig{RelayLogSummaryEvery: 0})

	var upstreamRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamRequests, 1)
		w.Header().Set("Content-Type", "video/mp2t")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	rawURL, err := url.Parse(server.URL + "/live/channel.ts")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	track := &m3u.Track{Name: "Channel", URI: rawURL.String()}

	sessionOne := manager.GetOrCreate(rawURL, http.Header{}, track)
	defer sessionOne.close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&upstreamRequests) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&upstreamRequests); got != 1 {
		t.Fatalf("upstream request count after first session = %d, want 1", got)
	}

	sessionTwo := manager.GetOrCreate(rawURL, http.Header{"Range": []string{"bytes=0-"}}, track)
	if sessionOne != sessionTwo {
		t.Fatal("GetOrCreate() returned different sessions for no-range and bytes=0-")
	}

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&upstreamRequests); got != 1 {
		t.Fatalf("upstream request count after second session = %d, want 1", got)
	}
}
