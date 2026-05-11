package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestConnectionTracker_TrackUntrack(t *testing.T) {
	tracker := &connectionTracker{conns: make(map[string]ActiveConnection)}

	id1 := tracker.track("http://upstream/live/1", "10.0.0.1")
	id2 := tracker.track("http://upstream/live/2", "10.0.0.2")

	active := tracker.active()
	if len(active) != 2 {
		t.Fatalf("expected 2 active connections, got %d", len(active))
	}

	tracker.untrack(id1)
	active = tracker.active()
	if len(active) != 1 {
		t.Fatalf("expected 1 active connection after untrack, got %d", len(active))
	}
	if active[0].ID != id2 {
		t.Errorf("expected remaining connection ID %s, got %s", id2, active[0].ID)
	}

	tracker.untrack(id2)
	active = tracker.active()
	if len(active) != 0 {
		t.Fatalf("expected 0 active connections after untrack all, got %d", len(active))
	}
}

func TestConnectionTracker_UntrackNonexistent(t *testing.T) {
	tracker := &connectionTracker{conns: make(map[string]ActiveConnection)}
	// Should not panic
	tracker.untrack("nonexistent")
}

func TestConnectionTracker_ActiveReturnsSnapshot(t *testing.T) {
	tracker := &connectionTracker{conns: make(map[string]ActiveConnection)}

	tracker.track("http://upstream/live/1", "10.0.0.1")
	snap := tracker.active()

	// Mutating the snapshot should not affect the tracker
	snap[0].URL = "mutated"
	original := tracker.active()
	if original[0].URL == "mutated" {
		t.Error("active() did not return a snapshot; mutation leaked to tracker")
	}
}

func TestHandleStatus_EmptyTracker(t *testing.T) {
	// Save and restore the global tracker
	saved := activeTracker
	activeTracker = &connectionTracker{conns: make(map[string]ActiveConnection)}
	defer func() { activeTracker = saved }()

	w := httptest.NewRecorder()
	ctx, _ := createTestContext(w)

	handleStatus(ctx)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.ActiveConnections != 0 {
		t.Errorf("expected 0 active connections, got %d", resp.ActiveConnections)
	}
	if resp.Connections == nil {
		t.Error("expected non-nil connections slice")
	}
}

func TestHandleStatus_WithActiveConnections(t *testing.T) {
	saved := activeTracker
	activeTracker = &connectionTracker{conns: make(map[string]ActiveConnection)}
	defer func() { activeTracker = saved }()

	activeTracker.track("http://upstream/live/1", "10.0.0.1")
	activeTracker.track("http://upstream/live/2", "10.0.0.2")

	w := httptest.NewRecorder()
	ctx, _ := createTestContext(w)

	handleStatus(ctx)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp statusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.ActiveConnections != 2 {
		t.Errorf("expected 2 active connections, got %d", resp.ActiveConnections)
	}
	if len(resp.Connections) != 2 {
		t.Errorf("expected 2 connections in list, got %d", len(resp.Connections))
	}
}

// createTestContext creates a minimal gin.Context for handler testing.
func createTestContext(w http.ResponseWriter) (*gin.Context, *gin.Engine) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	ctx := gin.CreateTestContextOnly(w, engine)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/status", nil)
	return ctx, engine
}
