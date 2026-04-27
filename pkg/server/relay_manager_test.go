package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jamesnetherton/m3u"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestRelayEligibilityReasons(t *testing.T) {
	tests := []struct {
		name       string
		track      *m3u.Track
		headers    http.Header
		wantOK     bool
		wantReason relayBypassReason
	}{
		{name: "no track", track: nil, headers: http.Header{}, wantOK: false, wantReason: relayBypassNoTrack},
		{name: "range request", track: &m3u.Track{URI: "http://provider/channel.ts"}, headers: http.Header{"Range": []string{"bytes=0-1"}}, wantOK: false, wantReason: relayBypassRange},
		{name: "hls", track: &m3u.Track{URI: "http://provider/channel.m3u8"}, headers: http.Header{}, wantOK: false, wantReason: relayBypassHLS},
		{name: "ineligible ext", track: &m3u.Track{URI: "http://provider/channel"}, headers: http.Header{}, wantOK: false, wantReason: relayBypassIneligibleExt},
		{name: "eligible ts", track: &m3u.Track{URI: "http://provider/channel.ts"}, headers: http.Header{}, wantOK: true, wantReason: relayBypassNone},
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
