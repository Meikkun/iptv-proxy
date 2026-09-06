package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	// Gin logs raw URLs during redirects before middleware can run. Configure
	// its global sinks once, before serving; application logs use log.Printf.
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard
}

type playlistFailure struct {
	kind  string
	cause error
}

func (e *playlistFailure) Error() string { return e.cause.Error() }
func (e *playlistFailure) Unwrap() error { return e.cause }

// Never format arbitrary wrapped errors: url.Error and transport errors may
// contain credentials in user-info, paths, queries, or redirect locations.
func safeErrorKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var playlistErr *playlistFailure
	if errors.As(err, &playlistErr) {
		return playlistErr.kind
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "timeout"
	}
	return "upstream_error"
}

func requestLogger() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		start := time.Now()
		ctx.Next()
		log.Printf("[iptv-proxy] method=%s status=%d duration=%s route=%s",
			safeRequestMethod(ctx.Request.Method), ctx.Writer.Status(), time.Since(start), relayKey(ctx.FullPath(), nil))
	}
}

func safeRequestMethod(method string) string {
	switch method {
	case "GET", "POST", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		return method
	default:
		return "OTHER"
	}
}

func safeRecovery() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		defer func() {
			if recover() != nil {
				log.Printf("[iptv-proxy] request panic")
				ctx.AbortWithStatus(500)
			}
		}()
		ctx.Next()
	}
}
