package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRelayStatusReportsPendingStarts(t *testing.T) {
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	defer upstream.Close()
	m := testRelayManager(t, testRelayConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sub, _ := m.subscribe(ctx, upstream.URL, nil)
		if sub != nil {
			sub.release()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("upstream not started")
	}
	if stats := m.stats(); stats.PendingStarts != 1 || stats.Sessions != 1 {
		t.Errorf("pending start missing: %+v", stats)
	}
	cancel()
	<-done
	m.Close()
}
