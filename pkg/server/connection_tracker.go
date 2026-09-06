package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ActiveConnection represents a single active streaming connection.
type ActiveConnection struct {
	ID           string `json:"id"`
	URL          string `json:"url"`
	ClientIP     string `json:"client_ip"`
	StartedAt    string `json:"started_at"`
	RequestedURL string `json:"requested_url,omitempty"`
	Substituted  bool   `json:"substituted,omitempty"`
}

// connectionTracker tracks active streaming connections.
type connectionTracker struct {
	mu    sync.RWMutex
	conns map[string]ActiveConnection
	seq   uint64
}

// activeTracker is the package-level connection tracker instance.
var activeTracker = &connectionTracker{
	conns: make(map[string]ActiveConnection),
}

// track registers a new active connection and returns its ID for later removal.
func (t *connectionTracker) track(upstreamURL, clientIP string) string {
	if len(upstreamURL) != 18 || !strings.HasPrefix(upstreamURL, "relay:") ||
		strings.Trim(upstreamURL[6:], "0123456789abcdef") != "" {
		upstreamURL = "direct:" + relayKey(upstreamURL, nil)[:12]
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	id := fmt.Sprintf("%d", t.seq)
	t.conns[id] = ActiveConnection{
		ID:        id,
		URL:       upstreamURL,
		ClientIP:  clientIP,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return id
}

// untrack removes a connection by ID.
func (t *connectionTracker) untrack(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.conns, id)
}

func (t *connectionTracker) selectRelay(id, requested, served string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	conn, ok := t.conns[id]
	if !ok {
		return
	}
	conn.URL = served
	conn.Substituted = requested != served
	if conn.Substituted {
		conn.RequestedURL = requested
	}
	t.conns[id] = conn
}

// active returns a snapshot of all active connections.
func (t *connectionTracker) active() []ActiveConnection {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]ActiveConnection, 0, len(t.conns))
	for _, c := range t.conns {
		result = append(result, c)
	}
	return result
}

// statusResponse is the JSON payload returned by the /status endpoint.
type statusResponse struct {
	ActiveConnections int                `json:"active_connections"`
	Connections       []ActiveConnection `json:"connections"`
	Relay             *relayStats        `json:"relay,omitempty"`
	Catalogue         *catalogueStatus   `json:"catalogue,omitempty"`
}

func (c *Config) status(ctx *gin.Context) {
	conns := activeTracker.active()
	response := statusResponse{ActiveConnections: len(conns), Connections: conns}
	if c.catalogue != nil {
		status := c.catalogue.status()
		response.Catalogue = &status
	}
	if c.relay != nil {
		stats := c.relay.stats()
		response.Relay = &stats
	}
	ctx.JSON(http.StatusOK, response)
}

// handleStatus returns the current active streaming connections as JSON.
func handleStatus(ctx *gin.Context) {
	(&Config{}).status(ctx)
}
