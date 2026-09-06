package server

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func relayConfiguration(c *config.ProxyConfig) config.RelayConfig {
	if c.Relay != nil {
		return *c.Relay
	}
	return config.DefaultRelayConfig()
}

type relayConnectionKey struct{}

// Plain HTTP/1 connections serve one response at a time. ConnContext supplies
// write deadlines on Go 1.17 without hijacking or changing chunked HTTP framing.
func relayConnContext(ctx context.Context, conn net.Conn) context.Context {
	return context.WithValue(ctx, relayConnectionKey{}, conn)
}

func relayEligible(req *http.Request, upstream *url.URL, extensionless bool) bool {
	if req.Method != http.MethodGet || req.ProtoMajor != 1 || (upstream.Scheme != "http" && upstream.Scheme != "https") {
		return false
	}
	ranges := req.Header.Values("Range")
	if len(ranges) > 0 && (len(ranges) != 1 || strings.TrimSpace(ranges[0]) != "bytes=0-") {
		return false
	}
	// Xtream playlists can label finite assets with EXTINF:-1 as well.
	upstreamPath := strings.ToLower(upstream.Path)
	for _, prefix := range []string{"/movie/", "/series/", "/timeshift/"} {
		if strings.HasPrefix(upstreamPath, prefix) {
			return false
		}
	}
	ext := path.Ext(upstream.Path)
	return strings.EqualFold(ext, ".ts") || (extensionless && ext == "")
}

func (c *Config) liveStream(ctx *gin.Context, upstream *url.URL, extensionless bool) {
	if c.relay == nil || !relayEligible(ctx.Request, upstream, extensionless) {
		c.xtreamStream(ctx, upstream)
		return
	}
	c.relayStream(ctx, upstream)
}

func (c *Config) relayStream(ctx *gin.Context, upstream *url.URL) {
	key := relayKey(upstream.String(), relayRequestHeaders(ctx.Request.Header))
	connID := activeTracker.track("relay:"+key[:12], ctx.ClientIP())
	defer activeTracker.untrack(connID)

	startupCtx, startupCancel := context.WithTimeout(ctx.Request.Context(), c.relay.startup)
	defer startupCancel()
	sub, status := c.relay.subscribe(startupCtx, upstream.String(), ctx.Request.Header)
	if sub == nil {
		if startupCtx.Err() == context.DeadlineExceeded && ctx.Request.Context().Err() == nil {
			status = http.StatusGatewayTimeout
		}
		if status == http.StatusConflict {
			ctx.AbortWithStatusJSON(status, gin.H{"error": "account is occupied by a stream with incompatible request credentials or headers"})
		} else {
			ctx.AbortWithStatus(status)
		}
		return
	}
	defer sub.release()
	activeTracker.selectRelay(connID, "relay:"+key[:12], "relay:"+sub.session.key[:12])

	// A late join during reconnection also has a bounded first-byte wait.
	var chunk []byte
	select {
	case chunk = <-sub.chunks:
	case <-sub.done:
		ctx.AbortWithStatus(http.StatusBadGateway)
		return
	case <-startupCtx.Done():
		ctx.AbortWithStatus(http.StatusGatewayTimeout)
		return
	}
	startupCancel()

	if conn, ok := ctx.Request.Context().Value(relayConnectionKey{}).(net.Conn); ok {
		// Wake a blocked writer immediately on eviction, cancellation or shutdown.
		stop, stopped := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(stopped)
			select {
			case <-sub.done:
				conn.SetWriteDeadline(time.Now())
			case <-ctx.Request.Context().Done():
				conn.SetWriteDeadline(time.Now())
			case <-stop:
			}
		}()
		defer func() {
			close(stop)
			<-stopped
			// Continuous relays end by disconnect, never by a reusable HTTP EOF.
			// Closing also prevents net/http's final flush from blocking after
			// a timed-out write.
			conn.Close()
		}()
	}

	mergeHttpHeader(ctx.Writer.Header(), sub.session.header)
	if key != sub.session.key {
		ctx.Header("X-IPTV-Relay-Substituted", "true")
	}
	ctx.Status(http.StatusOK)
	for {
		select {
		case <-ctx.Request.Context().Done():
			return
		case <-sub.done:
			return
		default:
		}
		if conn, ok := ctx.Request.Context().Value(relayConnectionKey{}).(net.Conn); ok {
			conn.SetWriteDeadline(time.Now().Add(relayWriteTimeout))
		}
		if _, err := ctx.Writer.Write(chunk); err != nil {
			return
		}
		ctx.Writer.Flush()
		select {
		case <-ctx.Request.Context().Done():
			return
		case <-sub.done:
			return
		case chunk = <-sub.chunks:
		}
	}
}
