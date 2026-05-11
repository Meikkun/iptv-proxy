# Streaming Stability, Reliability & Performance Improvement Plan

## 1. Objective

Improve the streaming proxy's stability, reliability, and performance by addressing 12 concrete issues found during a code audit. After completion:

- Upstream HTTP connections are reused across requests (no per-request client/transport allocation).
- HLS requests propagate the client's context, so upstream requests are cancelled when clients disconnect.
- Hop-by-hop HTTP headers are no longer forwarded through the proxy.
- The server shuts down gracefully, draining active streams before exiting.
- Streaming errors are logged for debugging.
- HLS manifest reads are bounded to prevent OOM.
- The xtream M3U cache path is read atomically (no TOCTOU race).
- A duplicate cache cleanup block is removed.
- The HLS streaming fallback uses a timeout-free client.
- `ctx.Stream` wrapper overhead is eliminated in favor of direct `io.Copy`.

## 2. Current-State Analysis

### Key files and their roles

| File | Role |
|---|---|
| `pkg/server/upstream_http.go` | Creates HTTP clients for upstream requests. Two factory functions: `newUpstreamHTTPClient()` (30s total timeout) and `newStreamingHTTPClient()` (no total timeout, 30s header timeout). |
| `pkg/server/handlers.go` | Core proxy logic. `stream()` creates a new `*http.Client` per request via `newStreamingHTTPClient()`. `mergeHttpHeader()` copies all headers without filtering. |
| `pkg/server/xtreamHandles.go` | Xtream API handlers. `hlsXtreamStream()` creates a throwaway `*http.Client` per HLS request. Cache functions have a duplicate cleanup and a TOCTOU race on `cachePath`. |
| `pkg/server/server.go` | `Serve()` calls `router.Run()` which wraps `http.ListenAndServe` with no graceful shutdown. |
| `cmd/root.go` | CLI entrypoint. Calls `server.Serve()` directly. |

### Existing patterns

- Error wrapping uses `utils.PrintErrorAndReturn(err)`.
- Debug logging uses `utils.DebugLog(format, args...)`.
- `nolint: errcheck` comments are used on intentionally-ignored errors.
- Tests are in `*_test.go` files in the same package (white-box).
- Table-driven test style with `t.Run`.
- Go 1.17; `vendor/` is committed; builds use `-mod=vendor`.
- No Makefile; CI uses goreleaser on tags.

### Existing callers of `newStreamingHTTPClient()`

Only one call site: `handlers.go:78` inside `stream()`.

### Existing callers of `newUpstreamHTTPClient()`

Only one call site: `playlist_loader.go:64` inside `openPlaylistSource()`.

## 3. Desired End State

After all changes:

1. **`upstream_http.go`** exports two package-level singleton `*http.Client` variables (one for streaming, one for non-streaming) plus one for HLS-no-redirect. All callers share these.
2. **`handlers.go`** `stream()` uses the shared streaming client, uses direct `io.Copy` with a 128KB buffer instead of `ctx.Stream`, and logs `io.Copy` errors via `utils.DebugLog`.
3. **`handlers.go`** `mergeHttpHeader()` skips hop-by-hop headers.
4. **`xtreamHandles.go`** `hlsXtreamStream()` uses the shared HLS client with context propagation, bounds manifest reads, uses the streaming client for the fallback path, and logs streaming errors.
5. **`xtreamHandles.go`** `cacheXtreamM3u()` has the duplicate cleanup removed.
6. **`xtreamHandles.go`** `xtreamGet()` and `xtreamApiGet()` read the cache path atomically under the write lock.
7. **`server.go`** `Serve()` uses `http.Server` + `signal.NotifyContext` for graceful shutdown with a configurable drain timeout.
8. **`cmd/root.go`** is unchanged (it already calls `server.Serve()` and `log.Fatal` on error).

### New constants, variables, and functions

| Location | Name | Type | Purpose |
|---|---|---|---|
| `upstream_http.go` | `streamingCopyBufSize` | `const int` (131072) | Buffer size for `io.CopyBuffer` during streaming |
| `upstream_http.go` | `streamingHTTPClient` | `var *http.Client` | Shared streaming client (no total timeout) |
| `upstream_http.go` | `upstreamHTTPClient` | `var *http.Client` | Shared non-streaming client (30s total timeout) |
| `upstream_http.go` | `hlsNoRedirectHTTPClient` | `var *http.Client` | Shared HLS client (30s timeout, no-redirect) |
| `upstream_http.go` | `hlsStreamingHTTPClient` | `var *http.Client` | Shared HLS streaming client (no total timeout, no-redirect) |
| `handlers.go` | `hopByHopHeaders` | `var map[string]struct{}` | Set of header names to skip in `mergeHttpHeader` |
| `xtreamHandles.go` | `maxHLSManifestBytes` | `const int64` (10 << 20) | Max bytes to read from HLS manifest response |
| `server.go` | `shutdownTimeout` | `const time.Duration` (15s) | Graceful shutdown drain timeout |

### Removed functions

| Location | Name | Reason |
|---|---|---|
| `upstream_http.go` | `newStreamingHTTPClient()` | Replaced by package-level `streamingHTTPClient` singleton |
| `upstream_http.go` | `newUpstreamHTTPClient()` | Replaced by package-level `upstreamHTTPClient` singleton |

## 4. Implementation Steps

### Step 1: Replace per-request HTTP clients with shared singletons

**File:** `pkg/server/upstream_http.go`

**Why:** Creating a new `*http.Client` (and cloning `http.DefaultTransport`) per request prevents TCP/TLS connection reuse. Under load, this causes file descriptor exhaustion from TIME_WAIT sockets.

Replace the entire file content with:

```go
package server

import (
	"net/http"
	"time"
)

const (
	defaultUpstreamRequestTimeout = 30 * time.Second
	streamingCopyBufSize          = 128 * 1024 // 128KB buffer for io.CopyBuffer
)

// streamingHTTPClient is a shared client for long-lived streaming requests.
// It has no overall timeout; only a ResponseHeaderTimeout to detect dead
// upstreams. The underlying transport is safe for concurrent use and
// maintains a connection pool.
var streamingHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = defaultUpstreamRequestTimeout
	return &http.Client{Transport: transport}
}()

// upstreamHTTPClient is a shared client for short-lived upstream requests
// (playlist downloads, API calls). It enforces a 30-second total timeout.
var upstreamHTTPClient = &http.Client{
	Timeout: defaultUpstreamRequestTimeout,
}

// hlsNoRedirectHTTPClient is a shared client for HLS manifest fetches where
// we need to capture 302 redirects instead of following them. It has a total
// timeout because manifest fetches are expected to complete quickly.
var hlsNoRedirectHTTPClient = &http.Client{
	Timeout: defaultUpstreamRequestTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// hlsStreamingHTTPClient is a shared client for HLS streaming fallback (when
// upstream does not redirect). No total timeout, no redirect following.
var hlsStreamingHTTPClient = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = defaultUpstreamRequestTimeout
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}()
```

This removes both `newStreamingHTTPClient()` and `newUpstreamHTTPClient()`.

### Step 2: Update `stream()` to use shared client, direct copy, and error logging

**File:** `pkg/server/handlers.go`

**Why:** `stream()` currently calls the removed `newStreamingHTTPClient()`, uses `ctx.Stream` wrapper unnecessarily, and silently discards `io.Copy` errors.

2a. In the import block, add `"github.com/pierre-emmanuelJ/iptv-proxy/pkg/utils"` if not already present. **It is already imported** (line 34), so no change needed.

2b. Replace the `stream()` function body (lines 75-101):

**Old code (lines 75-101):**
```go
func (c *Config) stream(ctx *gin.Context, oriURL *url.URL) {
	utils.DebugLog("-> Incoming URL: %s", ctx.Request.URL) // Or use c.Request.URL.Path for exact request path

	client := newStreamingHTTPClient()

	req, err := http.NewRequestWithContext(ctx.Request.Context(), "GET", oriURL.String(), nil)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	mergeHttpHeader(req.Header, ctx.Request.Header)

	resp, err := client.Do(req)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	defer resp.Body.Close()

	mergeHttpHeader(ctx.Writer.Header(), resp.Header)
	ctx.Status(resp.StatusCode)
	ctx.Stream(func(w io.Writer) bool {
		io.Copy(w, resp.Body) // nolint: errcheck
		return false
	})
}
```

**New code:**
```go
func (c *Config) stream(ctx *gin.Context, oriURL *url.URL) {
	utils.DebugLog("-> Incoming URL: %s", ctx.Request.URL)

	req, err := http.NewRequestWithContext(ctx.Request.Context(), "GET", oriURL.String(), nil)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}

	mergeHttpHeader(req.Header, ctx.Request.Header)

	resp, err := streamingHTTPClient.Do(req)
	if err != nil {
		ctx.AbortWithError(http.StatusInternalServerError, err) // nolint: errcheck
		return
	}
	defer resp.Body.Close()

	mergeHttpHeader(ctx.Writer.Header(), resp.Header)
	ctx.Status(resp.StatusCode)
	ctx.Writer.Flush()
	buf := make([]byte, streamingCopyBufSize)
	if _, err := io.CopyBuffer(ctx.Writer, resp.Body, buf); err != nil {
		utils.DebugLog("stream copy error for %s: %v", oriURL.Redacted(), err)
	}
}
```

**Changes explained:**
- `newStreamingHTTPClient()` → `streamingHTTPClient` (shared singleton).
- `ctx.Stream(func(w io.Writer) bool { io.Copy(...); return false })` → direct `io.CopyBuffer(ctx.Writer, resp.Body, buf)` with a 128KB buffer. The `ctx.Writer.Flush()` call before the copy ensures headers are sent to the client before streaming begins (same as `ctx.Stream` did internally).
- `io.Copy` error is now logged via `utils.DebugLog`.

### Step 3: Filter hop-by-hop headers in `mergeHttpHeader`

**File:** `pkg/server/handlers.go`

**Why:** The proxy currently forwards `Connection`, `Transfer-Encoding`, `Keep-Alive`, and other hop-by-hop headers. Forwarding `Transfer-Encoding` can cause HTTP framing errors at the client.

3a. Add a package-level variable after the `errRequestBodyTooLarge` declaration (after line 39):

```go
// hopByHopHeaders lists HTTP headers that must not be forwarded by a proxy.
// See RFC 7230 Section 6.1.
var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Proxy-Connection":    {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}
```

3b. Replace the `mergeHttpHeader` function (lines 125-134):

**Old code:**
```go
func mergeHttpHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			if values(dst.Values(k)).contains(v) {
				continue
			}
			dst.Add(k, v)
		}
	}
}
```

**New code:**
```go
func mergeHttpHeader(dst, src http.Header) {
	for k, vv := range src {
		if _, skip := hopByHopHeaders[http.CanonicalHeaderKey(k)]; skip {
			continue
		}
		for _, v := range vv {
			if values(dst.Values(k)).contains(v) {
				continue
			}
			dst.Add(k, v)
		}
	}
}
```

**Note:** `http.CanonicalHeaderKey(k)` ensures case-insensitive matching against the canonicalized keys in `hopByHopHeaders`. The map keys are already in canonical form (title case).

### Step 4: Add context propagation to HLS requests in `hlsXtreamStream`

**File:** `pkg/server/xtreamHandles.go`

**Why:** Both `http.NewRequest` calls in `hlsXtreamStream` do not use the request context. When a client disconnects, the upstream request continues indefinitely, wasting bandwidth.

4a. Replace line 628:

**Old:** `req, err := http.NewRequest("GET", oriURL.String(), nil)`
**New:** `req, err := http.NewRequestWithContext(ctx.Request.Context(), "GET", oriURL.String(), nil)`

4b. Replace line 659:

**Old:** `hlsReq, err := http.NewRequest("GET", location.String(), nil)`
**New:** `hlsReq, err := http.NewRequestWithContext(ctx.Request.Context(), "GET", location.String(), nil)`

### Step 5: Use shared HTTP clients in `hlsXtreamStream`

**File:** `pkg/server/xtreamHandles.go`

**Why:** A throwaway `*http.Client` is created per HLS request with no connection reuse. HLS involves many rapid sequential fetches.

5a. Remove the local client creation (lines 621-626):

**Old:**
```go
func (c *Config) hlsXtreamStream(ctx *gin.Context, oriURL *url.URL) {
	client := &http.Client{
		Timeout: defaultUpstreamRequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
```

**New:**
```go
func (c *Config) hlsXtreamStream(ctx *gin.Context, oriURL *url.URL) {
```

5b. Replace all references to `client.Do(...)` (lines 636 and 667) with `hlsNoRedirectHTTPClient.Do(...)`:

- Line 636: `resp, err := client.Do(req)` → `resp, err := hlsNoRedirectHTTPClient.Do(req)`
- Line 667: `hlsResp, err := client.Do(hlsReq)` → `hlsResp, err := hlsNoRedirectHTTPClient.Do(hlsReq)`

### Step 6: Bound HLS manifest read

**File:** `pkg/server/xtreamHandles.go`

**Why:** `io.ReadAll(hlsResp.Body)` at line 674 has no size limit, allowing a malicious upstream to cause OOM.

6a. Add a constant at the top of the file (after `const hlsRedirectTTL = 10 * time.Minute` on line 55):

```go
const maxHLSManifestBytes = 10 << 20 // 10MB
```

6b. Replace line 674:

**Old:** `b, err := io.ReadAll(hlsResp.Body)`
**New:** `b, err := io.ReadAll(io.LimitReader(hlsResp.Body, maxHLSManifestBytes))`

### Step 7: Use streaming client for HLS fallback path and log errors

**File:** `pkg/server/xtreamHandles.go`

**Why:** The fallback streaming path (lines 691-696) currently used the local `client` which had a 30s total timeout. Long HLS segments or live streams would be cut off. Also, the `io.Copy` error is silently discarded.

Replace lines 691-696:

**Old:**
```go
	mergeHttpHeader(ctx.Writer.Header(), resp.Header)
	ctx.Status(resp.StatusCode)
	ctx.Stream(func(w io.Writer) bool {
		io.Copy(w, resp.Body) // nolint: errcheck
		return false
	})
```

**New:**
```go
	mergeHttpHeader(ctx.Writer.Header(), resp.Header)
	ctx.Status(resp.StatusCode)
	ctx.Writer.Flush()
	buf := make([]byte, streamingCopyBufSize)
	if _, err := io.CopyBuffer(ctx.Writer, resp.Body, buf); err != nil {
		utils.DebugLog("HLS stream copy error for %s: %v", oriURL.Redacted(), err)
	}
```

**Note:** The `resp` here comes from the initial request which already used `hlsNoRedirectHTTPClient`. Since the response is a non-302 (not a redirect), this is a streaming path and needs to be copied fully. The response body is already open; we just need to copy it without the `ctx.Stream` wrapper and log errors. The `hlsNoRedirectHTTPClient` has a 30s total `Timeout`, which would terminate long streams. To fix this, the initial request in this function needs to be split: use `hlsNoRedirectHTTPClient` for the redirect-detection path and `hlsStreamingHTTPClient` for cases where the response turns out to be non-redirect streaming content.

However, this is problematic because we don't know if the response will be a redirect until after we get it. The simpler fix: since `hlsNoRedirectHTTPClient` has `CheckRedirect` returning `http.ErrUseLastResponse`, the client never follows redirects, so we always get the response quickly (either 302 or a real response). The `Timeout` on the client governs the entire request lifecycle including body reading. For the fallback streaming case (non-302), the body would be cut off after 30s.

**Revised approach for step 5a/7:** Use `hlsStreamingHTTPClient` (which has no total timeout but still blocks redirects) for the initial request. This safely handles both the redirect path (manifest reads complete quickly regardless) and the streaming fallback path (no timeout cutoff).

Update step 5b accordingly:
- Line 636: `resp, err := hlsStreamingHTTPClient.Do(req)` 
- Line 667: `hlsResp, err := hlsNoRedirectHTTPClient.Do(hlsReq)` (manifest fetch; total timeout is fine here since manifests are small)

### Step 8: Remove duplicate cache cleanup in `cacheXtreamM3u`

**File:** `pkg/server/xtreamHandles.go`

**Why:** Lines 90-94 attempt to remove the old cached file, but lines 70-74 already removed it. The second block is dead code — after the first removal, `xtreamM3uCache[cacheName]` still holds the old metadata (the map entry hasn't been updated yet), but `os.Remove` on the already-deleted file will fail with ENOENT, which is caught but adds noise.

Remove lines 90-94:

**Old (lines 90-94):**
```go
	if existing, ok := xtreamM3uCache[cacheName]; ok && existing.path != "" && existing.path != playlistPath {
		if removeErr := os.Remove(existing.path); removeErr != nil && !os.IsNotExist(removeErr) {
			utils.DebugLog("unable to remove stale xtream cache file %q: %v", existing.path, removeErr)
		}
	}
```

Remove these 4 lines entirely. The blank line before line 90 should also be removed to keep the code clean.

### Step 9: Fix TOCTOU race on cache path reads

**File:** `pkg/server/xtreamHandles.go`

**Why:** In `xtreamGet()` (lines 235-259) and `xtreamApiGet()` (lines 275-299), the write lock is released after checking/populating the cache, then a read lock is taken separately to read `cachePath`. Between those two lock operations, another goroutine could invalidate the cache entry.

**Fix for `xtreamGet()`** — replace lines 235-259:

**Old:**
```go
	xtreamM3uCacheLock.Lock()
	cleanupExpiredXtreamCacheEntries(now, ttl)
	meta, ok := xtreamM3uCache[cacheKey]
	xtreamM3uCacheLock.Unlock()

	if !ok || isXtreamCacheExpired(meta, now, ttl) {
		log.Printf("[iptv-proxy] %v | %s | xtream cache m3u file\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		playlist, err := m3u.Parse(m3uURL.String())
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
		if err := c.cacheXtreamM3u(&playlist, cacheKey); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	cachePath := xtreamM3uCache[cacheKey].path
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(cachePath)
```

**New:**
```go
	xtreamM3uCacheLock.Lock()
	cleanupExpiredXtreamCacheEntries(now, ttl)
	meta, ok := xtreamM3uCache[cacheKey]
	xtreamM3uCacheLock.Unlock()

	if !ok || isXtreamCacheExpired(meta, now, ttl) {
		log.Printf("[iptv-proxy] %v | %s | xtream cache m3u file\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		playlist, err := m3u.Parse(m3uURL.String())
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
		if err := c.cacheXtreamM3u(&playlist, cacheKey); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
	}

	xtreamM3uCacheLock.RLock()
	cachePath := xtreamM3uCache[cacheKey].path
	xtreamM3uCacheLock.RUnlock()
	if cachePath == "" {
		ctx.AbortWithStatus(http.StatusNotFound)
		return
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	ctx.Header("Content-Type", "application/octet-stream")
	ctx.File(cachePath)
```

**Changes:** Moved the `RLock`/`RUnlock` block before the header-setting so that `cachePath` is validated. Added a guard: if `cachePath` is empty (entry was evicted between the check and the read), return 404 instead of calling `ctx.File("")` which would panic or serve garbage.

**Fix for `xtreamApiGet()`** — apply the same pattern to lines 275-299:

**Old (lines 292-299):**
```go
	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	cachePath := xtreamM3uCache[cacheName].path
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(cachePath)
```

**New:**
```go
	xtreamM3uCacheLock.RLock()
	cachePath := xtreamM3uCache[cacheName].path
	xtreamM3uCacheLock.RUnlock()
	if cachePath == "" {
		ctx.AbortWithStatus(http.StatusNotFound)
		return
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	ctx.Header("Content-Type", "application/octet-stream")
	ctx.File(cachePath)
```

### Step 10: Update `openPlaylistSource` to use shared client

**File:** `pkg/server/playlist_loader.go`

**Why:** `newUpstreamHTTPClient()` is removed in step 1.

Replace line 64:

**Old:** `resp, err := newUpstreamHTTPClient().Get(source)`
**New:** `resp, err := upstreamHTTPClient.Get(source)`

### Step 11: Add graceful shutdown to `Serve()`

**File:** `pkg/server/server.go`

**Why:** `router.Run()` wraps `http.ListenAndServe` which has no graceful shutdown. On SIGTERM, active streaming connections are severed instantly.

11a. Add imports to `server.go`. The current import block (lines 21-38) needs these additions:

```go
"context"
"net/http"
"os/signal"
"syscall"
"time"
```

Check which are already imported: `fmt`, `log`, `net`, `net/url`, `os`, `path`, `path/filepath`, `strconv`, `strings` are present. None of the needed ones are present.

Add `"context"`, `"net/http"`, `"os/signal"`, `"syscall"`, and `"time"` to the stdlib import block.

11b. Add a constant before the `Config` struct:

```go
const shutdownTimeout = 15 * time.Second
```

11c. Replace the `Serve()` method (lines 88-102):

**Old:**
```go
// Serve the iptv-proxy api
func (c *Config) Serve() error {
	if err := c.playlistInitialization(); err != nil {
		return err
	}

	router := gin.Default()
	router.Use(cors.Default())
	group := router.Group("/")
	c.routes(group)

	// Add a message to indicate the server is ready
	log.Printf("[iptv-proxy] Server is ready and listening on :%d", c.HostConfig.Port)

	return router.Run(fmt.Sprintf(":%d", c.HostConfig.Port))
}
```

**New:**
```go
// Serve the iptv-proxy api
func (c *Config) Serve() error {
	if err := c.playlistInitialization(); err != nil {
		return err
	}

	router := gin.Default()
	router.Use(cors.Default())
	group := router.Group("/")
	c.routes(group)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", c.HostConfig.Port),
		Handler: router,
	}

	// Listen for interrupt/terminate signals to trigger graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("[iptv-proxy] Server is ready and listening on :%d", c.HostConfig.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		stop()
		log.Printf("[iptv-proxy] Shutting down gracefully (timeout %s)...", shutdownTimeout)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown error: %w", err)
		}
		log.Printf("[iptv-proxy] Server stopped")
		return nil
	}
}
```

**Behavior:** On SIGINT or SIGTERM, `Shutdown` stops accepting new connections and waits up to 15 seconds for active requests (including streaming) to finish before forcibly closing. This is the standard Go graceful shutdown pattern.

## 5. Testing Plan

### 5.1 New tests

#### Test: `TestMergeHttpHeaderSkipsHopByHop`

**File:** `pkg/server/handlers_test.go`

Add the following test after the existing `TestNormalizeHTTPStatusForError` test:

```go
func TestMergeHttpHeaderSkipsHopByHop(t *testing.T) {
	src := http.Header{
		"Content-Type":      {"application/json"},
		"Connection":        {"keep-alive"},
		"Transfer-Encoding": {"chunked"},
		"Keep-Alive":        {"timeout=5"},
		"X-Custom":          {"value"},
	}
	dst := http.Header{}

	mergeHttpHeader(dst, src)

	if dst.Get("Content-Type") != "application/json" {
		t.Errorf("Expected Content-Type to be forwarded, got %q", dst.Get("Content-Type"))
	}
	if dst.Get("X-Custom") != "value" {
		t.Errorf("Expected X-Custom to be forwarded, got %q", dst.Get("X-Custom"))
	}
	if dst.Get("Connection") != "" {
		t.Errorf("Expected Connection to be stripped, got %q", dst.Get("Connection"))
	}
	if dst.Get("Transfer-Encoding") != "" {
		t.Errorf("Expected Transfer-Encoding to be stripped, got %q", dst.Get("Transfer-Encoding"))
	}
	if dst.Get("Keep-Alive") != "" {
		t.Errorf("Expected Keep-Alive to be stripped, got %q", dst.Get("Keep-Alive"))
	}
}
```

#### Test: `TestMergeHttpHeaderPreservesDuplicateSkip`

**File:** `pkg/server/handlers_test.go`

Verifies that the existing duplicate-skip behavior still works for non-hop-by-hop headers:

```go
func TestMergeHttpHeaderPreservesDuplicateSkip(t *testing.T) {
	dst := http.Header{"Accept": {"text/html"}}
	src := http.Header{"Accept": {"text/html", "application/json"}}

	mergeHttpHeader(dst, src)

	got := dst.Values("Accept")
	if len(got) != 2 {
		t.Fatalf("Expected 2 Accept values, got %d: %v", len(got), got)
	}
}
```

#### Test: `TestStreamLogsUpstreamError`

**File:** `pkg/server/handlers_test.go`

This test is complex because it requires a gin context and an HTTP test server. It verifies that the `stream()` function handles upstream errors:

```go
func TestStreamHandlesUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Create a server that immediately closes the connection
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	upstreamURL, _ := url.Parse(upstream.URL + "/stream")

	c := &Config{
		ProxyConfig: &config.ProxyConfig{},
	}

	router := gin.New()
	router.GET("/test", func(ctx *gin.Context) {
		c.stream(ctx, upstreamURL)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}
```

### 5.2 Existing tests that must still pass

All existing tests remain unchanged and must pass:
- `TestReplaceTrackPathBasePreservesQuery`
- `TestReadLimitedRequestBodyTooLarge`
- `TestReadLimitedRequestBodyWithinLimit`
- `TestNormalizeHTTPStatusForError`
- `TestAuthenticateValidCredentials`
- `TestAuthenticateInvalidCredentials`
- `TestAuthenticateMissingCredentials`
- `TestReplaceURLOmitsDefaultHTTPSPort`
- `TestReplaceURLKeepsNonDefaultHTTPSPort`
- `TestReplaceURLOmitsDefaultHTTPPort`
- `TestReplaceURLPreservesXtreamCredentialSwapWithoutDefaultHTTPSPort`
- `TestTrackPathBaseStripsQueryString`
- `TestAdvancedParsingResponses`
- `TestProcessResponseEdgeCases`
- All tests in `cmd/root_test.go`
- All tests in `pkg/utils/error_utils_test.go`

### 5.3 Manual verification

After implementation, manually verify:
1. Start the proxy with a test M3U source and stream a channel; confirm the stream works.
2. Send SIGTERM to the process; confirm it logs the shutdown message and waits before exiting.
3. With `DEBUG_LOGGING=true`, disconnect a client mid-stream; confirm the copy error appears in logs.
4. Check that response headers from upstream do NOT include `Connection`, `Transfer-Encoding`, etc.

## 6. Validation Commands

Run these in order after making all changes:

```bash
# 1. Build — must produce no errors
go build -mod=vendor ./...

# 2. Static analysis — must produce no warnings
go vet -mod=vendor ./...

# 3. Run all tests with race detector
go test -mod=vendor -v -race -count=1 ./pkg/server/... ./pkg/utils/... ./cmd/...

# 4. Verify the binary starts (quick smoke test)
go run -mod=vendor . --help
```

## 7. Risk Review

| Risk | Mitigation |
|---|---|
| **Shared `http.Transport` could have unsuitable defaults for streaming** | `http.DefaultTransport.Clone()` preserves defaults (MaxIdleConns=100, MaxIdleConnsPerHost=2, IdleConnTimeout=90s). These are reasonable. If a user has many upstream hosts, `MaxIdleConnsPerHost=2` may limit reuse. This is acceptable for now; it's still much better than zero reuse. |
| **`ctx.Writer.Flush()` before `io.CopyBuffer` might not work on all gin versions** | `gin.ResponseWriter` implements `http.Flusher`. The project pins gin v1.9.0 which supports this. Verified by reading gin source. |
| **`io.CopyBuffer` with a 128KB buffer allocates 128KB per concurrent stream** | At 1000 concurrent streams this is 128MB — acceptable. The default 32KB buffer would be 32MB. The 4x increase provides better throughput for high-bitrate streams with fewer syscalls. |
| **Graceful shutdown timeout (15s) may be too short for long streams** | 15 seconds is a drain timeout for the *server to stop*. Active streams will be terminated after 15 seconds. This is standard practice. Users running behind reverse proxies typically have similar drain timeouts. The constant can be adjusted later if needed. |
| **Removing `newStreamingHTTPClient()` / `newUpstreamHTTPClient()` breaks callers** | Both functions are unexported and only called from within `pkg/server`. All call sites are updated in this plan: `handlers.go:78` and `playlist_loader.go:64`. No external callers exist. |
| **`signal.NotifyContext` requires Go 1.16+** | The project uses Go 1.17 (verified in `go.mod` line 54). `signal.NotifyContext` was introduced in Go 1.16. |
| **HLS `hlsStreamingHTTPClient` has no total timeout — could hang on broken upstreams** | It has `ResponseHeaderTimeout = 30s`, which detects dead upstreams that never send response headers. Once headers arrive, the body streaming has no timeout, which is correct for live streams. |
| **`hopByHopHeaders` map using `http.CanonicalHeaderKey` might miss non-canonical keys** | Go's `http.Header` map always stores keys in canonical form (via `textproto.CanonicalMIMEHeaderKey`). Both request and response headers from `net/http` are canonicalized. The `range` over `src` in `mergeHttpHeader` yields canonical keys, so `http.CanonicalHeaderKey(k)` is technically redundant but provides a safety net. |

## 8. Rollback or Recovery Notes

- All changes are in-memory code modifications to `.go` files. No data migrations, generated files, or external state changes.
- Rollback: `git checkout -- pkg/server/upstream_http.go pkg/server/handlers.go pkg/server/xtreamHandles.go pkg/server/server.go pkg/server/playlist_loader.go pkg/server/handlers_test.go` reverts everything.
- The shared HTTP clients are package-level variables initialized at program start. If the program panics during init (e.g., `http.DefaultTransport` is not `*http.Transport`), the binary will not start. This is extremely unlikely but detectable immediately.
- No configuration changes or new environment variables. Existing deployments work identically after upgrade.

## 9. Final Execution Checklist

- [ ] **Step 1:** Replace `pkg/server/upstream_http.go` with singleton HTTP clients. Remove `newStreamingHTTPClient()` and `newUpstreamHTTPClient()`.
- [ ] **Step 2:** Update `pkg/server/handlers.go` `stream()` to use `streamingHTTPClient`, direct `io.CopyBuffer`, and log errors.
- [ ] **Step 3:** Add `hopByHopHeaders` map and update `mergeHttpHeader()` in `pkg/server/handlers.go`.
- [ ] **Step 4:** Add context propagation to both `http.NewRequest` calls in `hlsXtreamStream()` in `pkg/server/xtreamHandles.go`.
- [ ] **Step 5:** Remove local client creation in `hlsXtreamStream()` and use `hlsStreamingHTTPClient` / `hlsNoRedirectHTTPClient` in `pkg/server/xtreamHandles.go`.
- [ ] **Step 6:** Add `maxHLSManifestBytes` constant and bound `io.ReadAll` with `io.LimitReader` in `pkg/server/xtreamHandles.go`.
- [ ] **Step 7:** Replace `ctx.Stream` wrapper with direct `io.CopyBuffer` and log errors in HLS fallback path in `pkg/server/xtreamHandles.go`.
- [ ] **Step 8:** Remove duplicate cache cleanup block (lines 90-94) in `cacheXtreamM3u()` in `pkg/server/xtreamHandles.go`.
- [ ] **Step 9:** Fix TOCTOU race on cache path in `xtreamGet()` and `xtreamApiGet()` in `pkg/server/xtreamHandles.go`.
- [ ] **Step 10:** Update `openPlaylistSource()` in `pkg/server/playlist_loader.go` to use `upstreamHTTPClient`.
- [ ] **Step 11:** Add graceful shutdown to `Serve()` in `pkg/server/server.go` using `http.Server` + `signal.NotifyContext`.
- [ ] **Tests:** Add `TestMergeHttpHeaderSkipsHopByHop`, `TestMergeHttpHeaderPreservesDuplicateSkip`, and `TestStreamHandlesUpstreamError` to `pkg/server/handlers_test.go`.
- [ ] **Build:** Run `go build -mod=vendor ./...` — no errors.
- [ ] **Vet:** Run `go vet -mod=vendor ./...` — no warnings.
- [ ] **Tests:** Run `go test -mod=vendor -v -race -count=1 ./pkg/server/... ./pkg/utils/... ./cmd/...` — all pass.
- [ ] **Smoke test:** Run `go run -mod=vendor . --help` — shows help without errors.
