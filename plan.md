# Implementation Plan: IPTV-Proxy Code Review Fixes

---

## 1. Objective

Implement all 18 findings from the code review. The fixes span security (authentication bypass, credential logging, URL injection), reliability (HTTP timeouts, temp file leaks, race conditions, panic guards), correctness (error location reporting, HLS body forwarding), build correctness (Dockerfile), and code quality (deprecated APIs, redundant code, naming).

After completion:
- Authentication middleware correctly blocks unauthenticated requests.
- No credentials appear in logs.
- HTTP clients have timeouts; upstream hangs cannot exhaust server resources.
- Temp M3U files are cleaned up on cache replacement.
- Error location reporting shows the actual call site.
- No panics on empty JSON input for `FlexFloat`/`JSONStringSlice`.
- HLS upstream non-302 responses forward their body.
- Dockerfile builds correctly with Go modules.
- All deprecated `ioutil` usage is replaced.
- All existing tests continue to pass; new tests cover the fixed behaviors.

---

## 2. Current-State Analysis

### Key Files and Their Roles

| File | Role |
|---|---|
| `cmd/root.go` | CLI entry, config loading, server startup |
| `pkg/config/config.go` | `ProxyConfig`, `HostConfiguration`, global vars `DebugLoggingEnabled`, `CacheFolder` |
| `pkg/server/server.go` | `Config` struct, `NewServer`, `Serve`, M3U marshalling, URL rewriting |
| `pkg/server/routes.go` | Gin route registration for M3U and Xtream endpoints |
| `pkg/server/handlers.go` | `authenticate`, `appAuthenticate`, `stream`, `reverseProxy`, `mergeHttpHeader` |
| `pkg/server/xtreamHandles.go` | Xtream API handlers, M3U caching, `ProcessResponse`, HLS streaming |
| `pkg/xtream-proxy/xtream-proxy.go` | Xtream client wrapper, `Action` dispatch, `login`, `validateParams` |
| `pkg/utils/debug.go` | `DebugLog` |
| `pkg/utils/error_utils.go` | `formatError`, `PrintErrorAndReturn`, `ErrorWithLocation` |
| `pkg/utils/error_utils_test.go` | Tests for error utils |
| `pkg/utils/file_utils.go` | `WriteResponseToFile`, `ConvertResponseToString` |
| `pkg/server/xtreamHandles_test.go` | Tests for `ProcessResponse` / advanced parsing |
| `vendor/github.com/tellytv/go.xtream-codes/xtream-codes.go` | Xtream API client implementation |
| `vendor/github.com/tellytv/go.xtream-codes/flex_types.go` | `FlexFloat`, `FlexInt`, `JSONStringSlice`, `Timestamp`, `ConvertibleBoolean` |
| `vendor/github.com/tellytv/go.xtream-codes/structs.go` | Xtream data structs, custom `UnmarshalJSON` methods |
| `vendor/github.com/tellytv/go.xtream-codes/debug.go` | Vendored `debugLog` |
| `Dockerfile` | Multi-stage Docker build |

### Existing Conventions
- Error handling: wrap with `utils.PrintErrorAndReturn(err)`, use `ctx.AbortWithError(...)`.
- Logging: `log.Printf("[iptv-proxy] ...")` for normal logs, `utils.DebugLog(...)` for debug.
- Gin middleware pattern: authenticate functions call `ctx.Bind` then check credentials.
- Tests use table-driven test style with `t.Run`.
- Vendored dependencies are committed in `vendor/`.

---

## 3. Desired End State

After all changes:

1. `authenticate` and `appAuthenticate` return immediately after denying access.
2. Route registration uses `PathEscape()` for credentials.
3. Old temp files are removed when cache entries are replaced.
4. `formatError` reports the correct caller location (2 frames up from `formatError`).
5. M3U cache uses proper locking to prevent races.
6. Dockerfile uses `GO111MODULE=on -mod=vendor`.
7. Vendored `go.xtream-codes` has its own `debugLog` and error formatting, no imports from `pkg/utils`.
8. All `http.Client{}` instances have a 30-second timeout.
9. `ProcessResponse` uses an interface check instead of string-based type name matching.
10. `xtreamGet` uses `url.Values` for query string construction.
11. Credentials are masked in log output.
12. `FlexFloat.UnmarshalJSON` and `JSONStringSlice.UnmarshalJSON` guard against empty input.
13. `hlsXtreamStream` forwards the upstream response body on non-302 status codes.
14. `getErrorDetailLevel` caches the env var at init time.
15. Redundant nil check in `cmd/root.go` is removed.
16. All `ioutil` usage replaced with `io` equivalents.
17. `ExampleErrorFormats` renamed to `logErrorFormats`.
18. New tests exist for authentication, URL construction, cache behavior, empty-input panic paths, and error location accuracy.

### New/Changed Files Summary

| File | Change Type |
|---|---|
| `pkg/server/handlers.go` | Modified (F1, F8, F17) |
| `pkg/server/routes.go` | Modified (F2) |
| `pkg/server/xtreamHandles.go` | Modified (F3, F5, F10, F11, F14, F17) |
| `pkg/utils/error_utils.go` | Modified (F4, F15) |
| `pkg/utils/error_utils_test.go` | Modified (F18, new tests for F4) |
| `cmd/root.go` | Modified (F12, F16) |
| `Dockerfile` | Modified (F6) |
| `vendor/.../flex_types.go` | Modified (F13) |
| `vendor/.../xtream-codes.go` | Modified (F7) |
| `vendor/.../debug.go` | Modified (F7) |
| `vendor/.../structs.go` | Modified (F7) |
| `pkg/server/handlers_test.go` | New (tests for F1, F2) |
| `pkg/server/xtreamHandles_test.go` | Modified (new tests for F10) |

---

## 4. Implementation Steps

Steps are ordered so that independent changes come first, and changes with cross-file dependencies are sequenced correctly.

---

### Step 1: Fix authentication bypass (F1)

**File:** `pkg/server/handlers.go`

**Change 1a:** At line 143, after `ctx.AbortWithStatus(http.StatusUnauthorized)`, add `return` on the same line or next line.

Replace:
```go
	if c.ProxyConfig.User.String() != authReq.Username || c.ProxyConfig.Password.String() != authReq.Password {
		ctx.AbortWithStatus(http.StatusUnauthorized)
	}
```
With:
```go
	if c.ProxyConfig.User.String() != authReq.Username || c.ProxyConfig.Password.String() != authReq.Password {
		ctx.AbortWithStatus(http.StatusUnauthorized)
		return
	}
```

**Change 1b:** At line 167, same fix in `appAuthenticate`.

Replace:
```go
	if c.ProxyConfig.User.String() != q["username"][0] || c.ProxyConfig.Password.String() != q["password"][0] {
		ctx.AbortWithStatus(http.StatusUnauthorized)
	}
```
With:
```go
	if c.ProxyConfig.User.String() != q["username"][0] || c.ProxyConfig.Password.String() != q["password"][0] {
		ctx.AbortWithStatus(http.StatusUnauthorized)
		return
	}
```

---

### Step 2: Replace deprecated `ioutil` usage (F17)

**File:** `pkg/server/handlers.go`

**Change 2a:** In the import block, remove `"io/ioutil"`. The file already imports `"io"` so no new import needed.

**Change 2b:** At line 148, replace `ioutil.ReadAll(ctx.Request.Body)` with `io.ReadAll(ctx.Request.Body)`.

**Change 2c:** At line 169, replace `ioutil.NopCloser(bytes.NewReader(contents))` with `io.NopCloser(bytes.NewReader(contents))`.

**File:** `pkg/server/xtreamHandles.go`

**Change 2d:** In the import block, remove `"io/ioutil"`. Add `"io"` to the import block (it is not currently imported in this file).

**Change 2e:** At line 239, replace `ioutil.ReadAll(ctx.Request.Body)` with `io.ReadAll(ctx.Request.Body)`.

**Change 2f:** At line 583, replace `ioutil.ReadAll(hlsResp.Body)` with `io.ReadAll(hlsResp.Body)`.

---

### Step 3: Add HTTP client timeouts (F8)

**File:** `pkg/server/handlers.go`

**Change 3a:** At line 69, replace:
```go
	client := &http.Client{}
```
With:
```go
	client := &http.Client{Timeout: 30 * time.Second}
```
The `time` package is already imported in this file.

**File:** `pkg/server/xtreamHandles.go`

**Change 3b:** At line 535, replace:
```go
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
```
With:
```go
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
```
The `time` package is already imported in this file.

---

### Step 4: Fix credentials in route paths (F2)

**File:** `pkg/server/routes.go`

**Change 4a:** At lines 61-68, replace every occurrence of `c.User` and `c.Password` in `fmt.Sprintf` route patterns with `c.User.PathEscape()` and `c.Password.PathEscape()`.

Replace lines 61-68:
```go
	r.GET(fmt.Sprintf("/%s/%s/:id", c.User, c.Password), c.xtreamStreamHandler)
	r.GET(fmt.Sprintf("/live/%s/%s/:id", c.User, c.Password), c.xtreamStreamLive)
	r.GET(fmt.Sprintf("/timeshift/%s/%s/:duration/:start/:id", c.User, c.Password), c.xtreamStreamTimeshift)
	r.GET(fmt.Sprintf("/movie/%s/%s/:id", c.User, c.Password), c.xtreamStreamMovie)
	r.GET(fmt.Sprintf("/series/%s/%s/:id", c.User, c.Password), c.xtreamStreamSeries)
	r.GET(fmt.Sprintf("/hlsr/:token/%s/%s/:channel/:hash/:chunk", c.User, c.Password), c.xtreamHlsrStream)
```
With:
```go
	r.GET(fmt.Sprintf("/%s/%s/:id", c.User.PathEscape(), c.Password.PathEscape()), c.xtreamStreamHandler)
	r.GET(fmt.Sprintf("/live/%s/%s/:id", c.User.PathEscape(), c.Password.PathEscape()), c.xtreamStreamLive)
	r.GET(fmt.Sprintf("/timeshift/%s/%s/:duration/:start/:id", c.User.PathEscape(), c.Password.PathEscape()), c.xtreamStreamTimeshift)
	r.GET(fmt.Sprintf("/movie/%s/%s/:id", c.User.PathEscape(), c.Password.PathEscape()), c.xtreamStreamMovie)
	r.GET(fmt.Sprintf("/series/%s/%s/:id", c.User.PathEscape(), c.Password.PathEscape()), c.xtreamStreamSeries)
	r.GET(fmt.Sprintf("/hlsr/:token/%s/%s/:channel/:hash/:chunk", c.User.PathEscape(), c.Password.PathEscape()), c.xtreamHlsrStream)
```

**Change 4b:** In `m3uRoutes` at line 83-85, apply the same fix:

Replace:
```go
		if strings.HasSuffix(track.URI, ".m3u8") {
			r.GET(fmt.Sprintf("/%s/%s/%s/%d/:id", c.endpointAntiColision, c.User, c.Password, i), trackConfig.m3u8ReverseProxy)
		} else {
			r.GET(fmt.Sprintf("/%s/%s/%s/%d/%s", c.endpointAntiColision, c.User, c.Password, i, path.Base(track.URI)), trackConfig.reverseProxy)
		}
```
With:
```go
		if strings.HasSuffix(track.URI, ".m3u8") {
			r.GET(fmt.Sprintf("/%s/%s/%s/%d/:id", c.endpointAntiColision, c.User.PathEscape(), c.Password.PathEscape(), i), trackConfig.m3u8ReverseProxy)
		} else {
			r.GET(fmt.Sprintf("/%s/%s/%s/%d/%s", c.endpointAntiColision, c.User.PathEscape(), c.Password.PathEscape(), i, path.Base(track.URI)), trackConfig.reverseProxy)
		}
```

---

### Step 5: Fix `xtreamGet` URL construction (F11)

**File:** `pkg/server/xtreamHandles.go`

Replace the `xtreamGet` function body (lines 148-193) with a version that uses `url.Values` for query construction:

Replace lines 149-159:
```go
	rawURL := fmt.Sprintf("%s/get.php?username=%s&password=%s", c.XtreamBaseURL, c.XtreamUser, c.XtreamPassword)

	q := ctx.Request.URL.Query()

	for k, v := range q {
		if k == "username" || k == "password" {
			continue
		}

		rawURL = fmt.Sprintf("%s&%s=%s", rawURL, k, strings.Join(v, ","))
	}
```
With:
```go
	q := ctx.Request.URL.Query()

	params := url.Values{}
	params.Set("username", c.XtreamUser.String())
	params.Set("password", c.XtreamPassword.String())
	for k, v := range q {
		if k == "username" || k == "password" {
			continue
		}
		for _, val := range v {
			params.Add(k, val)
		}
	}

	rawURL := fmt.Sprintf("%s/get.php?%s", c.XtreamBaseURL, params.Encode())
```

This preserves the existing behavior (filters out user-supplied username/password, passes through all other query params) but properly URL-encodes values.

---

### Step 6: Mask credentials in logs (F12)

**File:** `cmd/root.go`

Replace line 70:
```go
				log.Printf("[iptv-proxy] INFO: xtream service enable with xtream base url: %q xtream username: %q xtream password: %q", xtreamBaseURL, xtreamUser, xtreamPassword)
```
With:
```go
				log.Printf("[iptv-proxy] INFO: xtream service enable with xtream base url: %q xtream username: %q", xtreamBaseURL, xtreamUser)
```

This removes the password from the log line entirely. The username is kept for debugging purposes.

---

### Step 7: Remove redundant nil check (F16)

**File:** `cmd/root.go`

Replace lines 76-81:
```go
	if config.CacheFolder != "" {
		// Ensure CacheFolder ends with a '/'
		if config.CacheFolder != "" && !strings.HasSuffix(config.CacheFolder, "/") {
			config.CacheFolder += "/"
		}
	}
```
With:
```go
	// Ensure CacheFolder ends with a '/'
	if config.CacheFolder != "" && !strings.HasSuffix(config.CacheFolder, "/") {
		config.CacheFolder += "/"
	}
```

---

### Step 8: Fix `formatError` caller location (F4)

**File:** `pkg/utils/error_utils.go`

**Change 8a:** Modify `formatError` to accept a `skip` parameter instead of hardcoding `1`.

Replace the function signature and first few lines (lines 37-43):
```go
// formatError formats the error based on the detail level
func formatError(err error) error {
	if err == nil {
		return nil
	}

	// Get the caller information
	pc, file, line, ok := runtime.Caller(1)
```
With:
```go
// formatError formats the error based on the detail level.
// skip is the number of stack frames to skip (1 = formatError's caller, 2 = caller's caller).
func formatError(err error, skip int) error {
	if err == nil {
		return nil
	}

	// Get the caller information
	pc, file, line, ok := runtime.Caller(skip)
```

**Change 8b:** Update `ErrorWithLocation` at line 91 to pass `skip=2` (skip `formatError` + `ErrorWithLocation`):

Replace:
```go
func ErrorWithLocation(err error) error {
	if err == nil {
		return nil
	}
	return formatError(err)
}
```
With:
```go
func ErrorWithLocation(err error) error {
	if err == nil {
		return nil
	}
	return formatError(err, 2)
}
```

**Change 8c:** Update `PrintErrorAndReturn` at line 100 to pass `skip=2` (skip `formatError` + `PrintErrorAndReturn`):

Replace:
```go
	wrappedErr := formatError(err)
```
With:
```go
	wrappedErr := formatError(err, 2)
```

---

### Step 9: Cache `getErrorDetailLevel` at init time (F15)

**File:** `pkg/utils/error_utils.go`

**Change 9a:** Add a package-level variable and init function at the top of the file (after the `const` block, around line 22):

After the closing paren of the `const` block (line 21), add:
```go

var currentErrorDetailLevel ErrorDetailLevel

func init() {
	currentErrorDetailLevel = readErrorDetailLevel()
}
```

**Change 9b:** Rename the existing `getErrorDetailLevel` to `readErrorDetailLevel` (it reads from env) and create a new `getErrorDetailLevel` that returns the cached value:

Replace the existing `getErrorDetailLevel` function (lines 24-34):
```go
// getErrorDetailLevel returns the configured error detail level from environment
func getErrorDetailLevel() ErrorDetailLevel {
	level := strings.ToLower(os.Getenv("ERROR_DETAIL_LEVEL"))
	switch level {
	case "none":
		return ErrorDetailNone
	case "full":
		return ErrorDetailFull
	default:
		return ErrorDetailSimple // Default to simple error output
	}
}
```
With:
```go
// readErrorDetailLevel reads the error detail level from the environment variable.
func readErrorDetailLevel() ErrorDetailLevel {
	level := strings.ToLower(os.Getenv("ERROR_DETAIL_LEVEL"))
	switch level {
	case "none":
		return ErrorDetailNone
	case "full":
		return ErrorDetailFull
	default:
		return ErrorDetailSimple
	}
}

// getErrorDetailLevel returns the cached error detail level.
func getErrorDetailLevel() ErrorDetailLevel {
	return currentErrorDetailLevel
}
```

**Impact on tests:** The tests in `error_utils_test.go` set `ERROR_DETAIL_LEVEL` via `os.Setenv` and then call `getErrorDetailLevel()` or functions that use it. Because we now cache at init, those tests need to update the cached variable after setting the env var.

**Change 9c:** Export a `SetErrorDetailLevel` function for use in tests:

Add after the `getErrorDetailLevel` function:
```go
// SetErrorDetailLevel updates the cached error detail level. Intended for testing.
func SetErrorDetailLevel(level ErrorDetailLevel) {
	currentErrorDetailLevel = level
}
```

**File:** `pkg/utils/error_utils_test.go`

**Change 9d:** In `TestGetErrorDetailLevel`, after each `os.Setenv`, also call `currentErrorDetailLevel = readErrorDetailLevel()` to update the cache. Since `readErrorDetailLevel` is unexported but the test is in the same package (`utils`), this is accessible.

In the test loop body (around lines 44-53), replace:
```go
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("ERROR_DETAIL_LEVEL", tt.envValue)
			defer os.Unsetenv("ERROR_DETAIL_LEVEL")

			if got := getErrorDetailLevel(); got != tt.expectedLevel {
```
With:
```go
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("ERROR_DETAIL_LEVEL", tt.envValue)
			currentErrorDetailLevel = readErrorDetailLevel()
			defer os.Unsetenv("ERROR_DETAIL_LEVEL")

			if got := getErrorDetailLevel(); got != tt.expectedLevel {
```

**Change 9e:** Apply the same pattern in `TestErrorWithLocation` (line 119) and `TestPrintErrorAndReturn` (line 185) and `ExampleErrorFormats` (lines 242, 247, 253) -- after every `os.Setenv("ERROR_DETAIL_LEVEL", ...)`, add `currentErrorDetailLevel = readErrorDetailLevel()` on the next line.

Specifically:
- Line 119: after `os.Setenv("ERROR_DETAIL_LEVEL", tt.detailLevel)`, add `currentErrorDetailLevel = readErrorDetailLevel()`
- Line 185: after `os.Setenv("ERROR_DETAIL_LEVEL", tt.detailLevel)`, add `currentErrorDetailLevel = readErrorDetailLevel()`
- Line 242: after `os.Setenv("ERROR_DETAIL_LEVEL", "simple")`, add `currentErrorDetailLevel = readErrorDetailLevel()`
- Line 247: after `os.Setenv("ERROR_DETAIL_LEVEL", "full")`, add `currentErrorDetailLevel = readErrorDetailLevel()`
- Line 253: after `os.Setenv("ERROR_DETAIL_LEVEL", "none")`, add `currentErrorDetailLevel = readErrorDetailLevel()`

---

### Step 10: Rename `ExampleErrorFormats` (F18)

**File:** `pkg/utils/error_utils_test.go`

**Change 10a:** Rename the function from `ExampleErrorFormats` to `logErrorFormats` (line 228):

Replace:
```go
func ExampleErrorFormats(t *testing.T) {
```
With:
```go
func logErrorFormats(t *testing.T) {
```

**Change 10b:** Update the call site at line 59:

Replace:
```go
		ExampleErrorFormats(t)
```
With:
```go
		logErrorFormats(t)
```

---

### Step 11: Add temp file cleanup (F3)

**File:** `pkg/server/xtreamHandles.go`

**Change 11a:** In `cacheXtreamM3u` (lines 58-78), before creating the new cache entry, check if an old file exists for this cache key and delete it.

After `xtreamM3uCacheLock.Lock()` (line 59) and before the line `tmp := *c` (line 62), insert:
```go
	// Clean up the old cached file if one exists for this key
	if old, exists := xtreamM3uCache[cacheName]; exists {
		if old.string != "" {
			os.Remove(old.string)
		}
	}
```

The `os` package is already imported in this file. This ensures the old temp file is removed before we create a new one.

---

### Step 12: Fix M3U cache race condition (F5)

**File:** `pkg/server/xtreamHandles.go`

The current pattern in `xtreamGet` (and `xtreamApiGet`) is:
1. RLock, check cache, RUnlock
2. If miss: fetch, call cacheXtreamM3u (which takes write lock)
3. RLock, read path, RUnlock
4. Serve file

The race: between step 2 and step 3, another goroutine could replace the entry. Also, two concurrent cache misses both fetch.

**Change 12a:** Replace the `xtreamGet` method's cache logic (lines 167-192) with a pattern that uses the write lock for the miss path and serves directly from the known path:

Replace lines 167-192:
```go
	xtreamM3uCacheLock.RLock()
	meta, ok := xtreamM3uCache[m3uURL.String()]
	d := time.Since(meta.Time)
	if !ok || d.Hours() >= float64(c.M3UCacheExpiration) {
		log.Printf("[iptv-proxy] %v | %s | xtream cache m3u file\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		xtreamM3uCacheLock.RUnlock()
		playlist, err := m3u.Parse(m3uURL.String())
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
		if err := c.cacheXtreamM3u(&playlist, m3uURL.String()); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
	} else {
		xtreamM3uCacheLock.RUnlock()
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	path := xtreamM3uCache[m3uURL.String()].string
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(path)
```
With:
```go
	cacheKey := m3uURL.String()

	xtreamM3uCacheLock.RLock()
	meta, ok := xtreamM3uCache[cacheKey]
	cachedPath := meta.string
	expired := !ok || time.Since(meta.Time).Hours() >= float64(c.M3UCacheExpiration)
	xtreamM3uCacheLock.RUnlock()

	if expired {
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
		xtreamM3uCacheLock.RLock()
		cachedPath = xtreamM3uCache[cacheKey].string
		xtreamM3uCacheLock.RUnlock()
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	ctx.Header("Content-Type", "application/octet-stream")
	ctx.File(cachedPath)
```

**Change 12b:** Apply the same pattern to `xtreamApiGet` (lines 205-231):

Replace lines 205-231:
```go
	xtreamM3uCacheLock.RLock()
	meta, ok := xtreamM3uCache[cacheName]
	d := time.Since(meta.Time)
	if !ok || d.Hours() >= float64(c.M3UCacheExpiration) {
		log.Printf("[iptv-proxy] %v | %s | xtream cache API m3u file\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		xtreamM3uCacheLock.RUnlock()
		playlist, err := c.xtreamGenerateM3u(ctx, extension)
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
		if err := c.cacheXtreamM3u(playlist, cacheName); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
	} else {
		xtreamM3uCacheLock.RUnlock()
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	xtreamM3uCacheLock.RLock()
	path := xtreamM3uCache[cacheName].string
	xtreamM3uCacheLock.RUnlock()
	ctx.Header("Content-Type", "application/octet-stream")

	ctx.File(path)
```
With:
```go
	xtreamM3uCacheLock.RLock()
	meta, ok := xtreamM3uCache[cacheName]
	cachedPath := meta.string
	expired := !ok || time.Since(meta.Time).Hours() >= float64(c.M3UCacheExpiration)
	xtreamM3uCacheLock.RUnlock()

	if expired {
		log.Printf("[iptv-proxy] %v | %s | xtream cache API m3u file\n", time.Now().Format("2006/01/02 - 15:04:05"), ctx.ClientIP())
		playlist, err := c.xtreamGenerateM3u(ctx, extension)
		if err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
		if err := c.cacheXtreamM3u(playlist, cacheName); err != nil {
			ctx.AbortWithError(http.StatusInternalServerError, utils.PrintErrorAndReturn(err)) // nolint: errcheck
			return
		}
		xtreamM3uCacheLock.RLock()
		cachedPath = xtreamM3uCache[cacheName].string
		xtreamM3uCacheLock.RUnlock()
	}

	ctx.Header("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, c.M3UFileName))
	ctx.Header("Content-Type", "application/octet-stream")
	ctx.File(cachedPath)
```

---

### Step 13: Fix `ProcessResponse` type matching (F10)

**File:** `pkg/server/xtreamHandles.go`

**Change 13a:** Replace the `ProcessResponse` function (lines 285-299):
```go
// ProcessResponse processes various types of xtream-codes responses
func ProcessResponse(resp interface{}) interface{} {

	respType := reflect.TypeOf(resp)

	switch {
	case respType == nil:
		return resp
	case strings.Contains(respType.String(), "[]xtreamcodes."):
		return processXtreamArray(resp)
	case strings.Contains(respType.String(), "xtreamcodes."):
		return processXtreamStruct(resp)
	default:
	}
	return resp
}
```
With:
```go
// ProcessResponse processes various types of xtream-codes responses
func ProcessResponse(resp interface{}) interface{} {
	if resp == nil {
		return resp
	}

	v := reflect.ValueOf(resp)

	// Handle slices
	if v.Kind() == reflect.Slice {
		if v.Len() > 0 && isXtreamCodesStruct(v.Index(0).Interface()) {
			return processXtreamArray(resp)
		}
		return resp
	}

	// Handle structs and pointers to structs
	if isXtreamCodesStruct(resp) {
		return processXtreamStruct(resp)
	}

	return resp
}
```

**Change 13b:** Update `isXtreamCodesStruct` to not use string-based type checks (lines 336-339):

Replace:
```go
func isXtreamCodesStruct(item interface{}) bool {
	respType := reflect.TypeOf(item)
	return respType != nil && strings.Contains(respType.String(), "xtreamcodes.") && hasFieldsField(item)
}
```
With:
```go
func isXtreamCodesStruct(item interface{}) bool {
	return item != nil && hasFieldsField(item)
}
```

---

### Step 14: Fix `FlexFloat` and `JSONStringSlice` empty input panics (F13)

**File:** `vendor/github.com/tellytv/go.xtream-codes/flex_types.go`

**Change 14a:** In `FlexFloat.UnmarshalJSON` (line 108), add a length guard:

Replace:
```go
func (ff *FlexFloat) UnmarshalJSON(b []byte) error {
	if b[0] != '"' {
```
With:
```go
func (ff *FlexFloat) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	if b[0] != '"' {
```

**Change 14b:** In `JSONStringSlice.UnmarshalJSON` (line 147), add a length guard:

Replace:
```go
func (b *JSONStringSlice) UnmarshalJSON(data []byte) error {
	if data[0] == '"' {
```
With:
```go
func (b *JSONStringSlice) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if data[0] == '"' {
```

---

### Step 15: Fix `hlsXtreamStream` missing body on non-302 (F14)

**File:** `pkg/server/xtreamHandles.go`

Replace line 600-601 (the `else` branch at the end of `hlsXtreamStream`):
```go
	ctx.Status(resp.StatusCode)
```
With:
```go
	mergeHttpHeader(ctx.Writer.Header(), resp.Header)
	ctx.Status(resp.StatusCode)
	ctx.Stream(func(w io.Writer) bool {
		io.Copy(w, resp.Body) // nolint: errcheck
		return false
	})
```

This requires `io` to be imported. After Step 2d, `io` will already be imported in this file.

---

### Step 16: Fix Dockerfile build command (F6)

**File:** `Dockerfile`

Replace line 7:
```
RUN GO111MODULE=off CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o iptv-proxy .
```
With:
```
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -a -installsuffix cgo -o iptv-proxy .
```

Note: In Go 1.17, `GO111MODULE` defaults to `on`, so we don't need to set it explicitly. The `-mod=vendor` flag tells Go to use the `vendor/` directory.

---

### Step 17: Decouple vendored library from parent project (F7)

This step removes the imports of `github.com/pierre-emmanuelJ/iptv-proxy/pkg/utils` and `github.com/gin-gonic/gin` from the vendored `go.xtream-codes` library by inlining the minimal needed functionality.

**File:** `vendor/github.com/tellytv/go.xtream-codes/debug.go`

This file already has its own `debugLog` function. No changes needed here.

**File:** `vendor/github.com/tellytv/go.xtream-codes/xtream-codes.go`

**Change 17a:** Replace the import of `"github.com/gin-gonic/gin"` and `"github.com/pierre-emmanuelJ/iptv-proxy/pkg/utils"` with the locally needed functionality.

Replace the import block (lines 4-19):
```go
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/buger/jsonparser"
	"github.com/gin-gonic/gin"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/utils"
)
```
With:
```go
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/buger/jsonparser"
)
```

**Change 17b:** Replace all calls to `utils.PrintErrorAndReturn(err)` in this file with just `err` (or with a local `logError` helper). There are usages at lines 76, 167, 233, 303, 319, 349, 379, 425, and 427.

Add a local helper function after the `debugLog` function in `debug.go` or at the top of `xtream-codes.go` (after the imports):

Add the helper in `xtream-codes.go` right after the `init()` block (after line 28):

```go
// logError logs the error to stderr and returns it. Local replacement for utils.PrintErrorAndReturn.
func logError(err error) error {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return err
}
```

Then replace all occurrences of `utils.PrintErrorAndReturn(...)` in `xtream-codes.go` with `logError(...)`. There are these occurrences:
- Line 76: `return nil, utils.PrintErrorAndReturn(fmt.Errorf(...))` -> `return nil, logError(fmt.Errorf(...))`
- Line 167: `utils.PrintErrorAndReturn(err)` -> `logError(err)` (return value discarded)
- Line 182-184: `utils.PrintErrorAndReturn(jsonErr)` -> `logError(jsonErr)` (return value discarded)
- Line 233: `utils.PrintErrorAndReturn(err)` -> `logError(err)` (return value discarded)
- Line 272: `return nil, utils.PrintErrorAndReturn(jsonErr)` -> `return nil, logError(jsonErr)`
- Line 303: `utils.PrintErrorAndReturn(err)` -> `logError(err)` (return value discarded)
- Line 319: `utils.PrintErrorAndReturn(jsonErr)` -> `logError(jsonErr)` (return value discarded)
- Line 349: `utils.PrintErrorAndReturn(jsonErr)` -> `logError(jsonErr)` (return value discarded)
- Line 379: `utils.PrintErrorAndReturn(jsonErr)` -> `logError(jsonErr)` (return value discarded)
- Line 425-427: `utils.PrintErrorAndReturn(jsonErr)` -> `logError(jsonErr)` (return value discarded)

**Change 17c:** Remove the `gin.Context` mock and `utils.WriteResponseToFile` call in `sendRequestWithURL` (lines 478-488):

Replace lines 478-488:
```go
	// Create a mock gin.Context for the WriteResponseToFile function
	mockCtx := &gin.Context{
		Request: &http.Request{
			URL: request.URL,
		},
	}

	contentType := strings.Split(response.Header.Get("Content-Type"), ";")[0]

	// Write response to file
	utils.WriteResponseToFile(mockCtx, buf.Bytes(), contentType)
```
With:
```go
	// Cache response to file if configured
	cacheFolder := os.Getenv("CACHE_FOLDER")
	if cacheFolder != "" {
		contentType := strings.Split(response.Header.Get("Content-Type"), ";")[0]
		writeResponseCache(request.URL.String(), buf.Bytes(), contentType, cacheFolder)
	}
```

Add a new helper function at the end of `xtream-codes.go`:

```go
// writeResponseCache writes the response body to a cache file.
func writeResponseCache(requestURL string, data []byte, contentType, cacheFolder string) {
	var extension string
	switch contentType {
	case "application/json":
		extension = ".json"
	case "application/xml", "text/xml":
		extension = ".xml"
	default:
		extension = ".json"
	}

	if cacheFolder != "" && !strings.HasSuffix(cacheFolder, "/") {
		cacheFolder += "/"
	}

	if err := os.MkdirAll(cacheFolder, 0755); err != nil {
		debugLog("Error creating cache directory: %v", err)
		return
	}

	filename := cacheFolder + url.QueryEscape(requestURL) + extension
	if _, err := os.Stat(filename); err == nil {
		return // File already exists, don't overwrite
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		debugLog("Error writing cache file: %v", err)
	}
}
```

**File:** `vendor/github.com/tellytv/go.xtream-codes/flex_types.go`

**Change 17d:** Remove the import of `"github.com/pierre-emmanuelJ/iptv-proxy/pkg/utils"`.

Replace the import block (lines 3-12):
```go
import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/utils"
)
```
With:
```go
import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)
```

**Change 17e:** Replace `utils.PrintErrorAndReturn(err)` calls in `flex_types.go`:

- Line 100 (`FlexInt.UnmarshalJSON`): `return utils.PrintErrorAndReturn(err)` -> `return logError(err)`
- Line 115 (`FlexFloat.UnmarshalJSON`): `return utils.PrintErrorAndReturn(err)` -> `return logError(err)`

The `logError` function is defined in `xtream-codes.go` which is in the same package, so it's accessible.

**File:** `vendor/github.com/tellytv/go.xtream-codes/structs.go`

**Change 17f:** This file does not import `utils` or `gin`. It only uses `debugLog` which is in the same package. No changes needed.

**File:** `vendor/modules.txt`

**Change 17g:** Remove the line referencing `github.com/pierre-emmanuelJ/iptv-proxy/pkg/utils` from the `go.xtream-codes` dependencies if it appears. Read the file first, then remove the relevant line.

Also verify: after removing `gin` from `xtream-codes.go`, check if `vendor/modules.txt` lists `gin` as a dependency of `go.xtream-codes`. If so, remove that reference too. Note: `gin` is still needed by the main project, so only remove it from the `go.xtream-codes` section.

---

### Step 18: Create new test file for authentication and route tests

**File:** `pkg/server/handlers_test.go` (new file)

Create this file with the following tests:

```go
package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestAuthenticateValidCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := &Config{
		ProxyConfig: &config.ProxyConfig{
			User:     "testuser",
			Password: "testpass",
		},
	}

	router := gin.New()
	router.GET("/test", c.authenticate, func(ctx *gin.Context) {
		ctx.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test?username=testuser&password=testpass", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestAuthenticateInvalidCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := &Config{
		ProxyConfig: &config.ProxyConfig{
			User:     "testuser",
			Password: "testpass",
		},
	}

	router := gin.New()
	router.GET("/test", c.authenticate, func(ctx *gin.Context) {
		ctx.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test?username=wrong&password=wrong", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", w.Code)
	}
}

func TestAuthenticateMissingCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c := &Config{
		ProxyConfig: &config.ProxyConfig{
			User:     "testuser",
			Password: "testpass",
		},
	}

	router := gin.New()
	router.GET("/test", c.authenticate, func(ctx *gin.Context) {
		ctx.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}
```

---

### Step 19: Add `ProcessResponse` edge case tests

**File:** `pkg/server/xtreamHandles_test.go`

Add the following test function after `TestAdvancedParsingResponses`:

```go
func TestProcessResponseEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		wantNil  bool
	}{
		{
			name:    "nil input returns nil",
			input:   nil,
			wantNil: true,
		},
		{
			name:  "string input returned as-is",
			input: "hello",
		},
		{
			name:  "int input returned as-is",
			input: 42,
		},
		{
			name:  "empty slice returned as-is",
			input: []xtream.Category{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProcessResponse(tt.input)
			if tt.wantNil {
				if result != nil {
					t.Errorf("Expected nil, got %v", result)
				}
				return
			}
			if result == nil {
				t.Error("Expected non-nil result")
			}
		})
	}
}
```

---

### Step 20: Add error location accuracy test

**File:** `pkg/utils/error_utils_test.go`

Add the following test function after `TestPrintErrorAndReturn`:

```go
func TestFormatErrorReportsCallerLocation(t *testing.T) {
	os.Setenv("ERROR_DETAIL_LEVEL", "simple")
	currentErrorDetailLevel = readErrorDetailLevel()
	defer os.Unsetenv("ERROR_DETAIL_LEVEL")

	err := errors.New("test error")
	wrapped := PrintErrorAndReturn(err)

	if wrapped == nil {
		t.Fatal("Expected non-nil error")
	}

	errStr := wrapped.Error()
	// The error should report this test file, NOT error_utils.go
	if strings.Contains(errStr, "error_utils.go") {
		t.Errorf("Error location should report the caller, not error_utils.go. Got: %s", errStr)
	}
	if !strings.Contains(errStr, "error_utils_test.go") {
		t.Errorf("Error location should contain error_utils_test.go. Got: %s", errStr)
	}
}
```

---

### Step 21: Update `vendor/modules.txt`

**File:** `vendor/modules.txt`

Read the file, then remove any lines that reference `github.com/pierre-emmanuelJ/iptv-proxy/pkg/utils` under the `go.xtream-codes` section, and remove `github.com/gin-gonic/gin` if it appears as a dependency listed under the `go.xtream-codes` vendor section. Be careful to only remove references from the xtream-codes section, not from the main module's section.

---

## 5. Testing Plan

### Existing Tests (must still pass)
| Test File | Command |
|---|---|
| `pkg/server/xtreamHandles_test.go` | `go test ./pkg/server/...` |
| `pkg/utils/error_utils_test.go` | `go test ./pkg/utils/...` |
| Vendored library tests | `go test ./vendor/github.com/tellytv/go.xtream-codes/...` (if runnable) |

### New Tests

| Test | File | What It Validates |
|---|---|---|
| `TestAuthenticateValidCredentials` | `pkg/server/handlers_test.go` | Valid creds -> 200 |
| `TestAuthenticateInvalidCredentials` | `pkg/server/handlers_test.go` | Wrong creds -> 401, handler chain stops |
| `TestAuthenticateMissingCredentials` | `pkg/server/handlers_test.go` | No creds -> 400 |
| `TestProcessResponseEdgeCases` | `pkg/server/xtreamHandles_test.go` | nil, string, int, empty slice inputs |
| `TestFormatErrorReportsCallerLocation` | `pkg/utils/error_utils_test.go` | Error reports caller file, not error_utils.go |

### Manual Verification

1. Build the Docker image: `docker build -t iptv-proxy-test .` -- verify it succeeds with the new Dockerfile.
2. Start the container with test config, verify the proxy starts and logs do NOT contain passwords.
3. Verify temp files in `/tmp/` are cleaned up after cache expiration by checking the file count before and after a cache miss cycle (if a test upstream is available).

---

## 6. Validation Commands

Run these in order from the project root (`/home/mdn/Code/iptv-proxy`):

```bash
# 1. Verify the code compiles
go build ./...

# 2. Run all tests (set a long timeout; tests may need network)
go test -v -count=1 -timeout 120s ./pkg/server/... ./pkg/utils/...

# 3. Run go vet for static analysis
go vet ./...

# 4. Verify Docker build succeeds
docker build -t iptv-proxy-test .

# 5. Verify vendor consistency (optional, may need network)
# go mod vendor && go mod verify
```

Note: `go test` and `go vet` timed out during the review (>60s). Use a 120s timeout. If tests that hit external network endpoints are present, they may need `USE_XTREAM_ADVANCED_PARSING=true` set.

---

## 7. Risk Review

| Risk | Mitigation |
|---|---|
| **Step 4 (PathEscape in routes) may double-encode if credentials are already safe** | `url.PathEscape` on simple alphanumeric strings is a no-op (returns the same string). No double-encoding risk for typical credentials. |
| **Step 8 (formatError skip depth) may report wrong frame if call chain changes** | The skip value of 2 is correct for the current two-layer call chain (`PrintErrorAndReturn` -> `formatError`). Adding a new wrapper would need its own skip adjustment, which is an accepted tradeoff vs. the current state (always wrong). |
| **Step 9 (cached error level) means env var changes at runtime are not picked up** | This matches the behavior of `config.DebugLoggingEnabled` which is also set once at startup. Document this in the function comment. Tests are updated to explicitly set the cached value. |
| **Step 11 (temp file cleanup) could delete a file still being served** | `os.Remove` on Linux only unlinks the file; any open file descriptors (from `ctx.File`) continue to work until the descriptor is closed. No data corruption risk. |
| **Step 12 (cache race fix) still allows duplicate fetches** | Two concurrent cache misses will both fetch. This is acceptable; a full singleflight implementation would be more complex and is out of scope. The fix prevents the read-after-write race. |
| **Step 17 (vendored lib decoupling) introduces code duplication** | The duplicated code is ~15 lines (`logError`, `writeResponseCache`). This is acceptable to break the circular dependency. The duplication is small and the functions are simple. |
| **Step 17 may break `vendor/modules.txt` consistency** | After modifying vendored code, `vendor/modules.txt` must be updated to remove stale dependency lines. The executing model should verify with `go build ./...` after changes. |
| **FlexFloat empty guard (Step 14) silently returns nil on empty input** | This matches the existing `FlexInt` behavior (line 92-94 of flex_types.go) where empty data returns nil. Consistent behavior across flex types. |
| **Dockerfile change (Step 16) may fail if `vendor/` is incomplete** | Compilation already succeeded with the current vendor directory. The `-mod=vendor` flag just makes the module system use vendor explicitly rather than relying on GOPATH fallback. |

---

## 8. Rollback or Recovery Notes

- All changes are to source files and vendored code tracked in git. Rollback is `git checkout -- .` or reverting the commit.
- No database migrations, generated files, or deployment state changes are involved.
- The vendored library changes are local modifications to checked-in vendor code. If upstream changes need to be pulled in later, the modifications to `xtream-codes.go`, `flex_types.go`, and `structs.go` must be reapplied (or the circular dependency must be avoided in the upstream fork).
- The new test file `pkg/server/handlers_test.go` can simply be deleted if rolled back.
- Docker image changes only affect new builds; existing running containers are unaffected.

---

## 9. Final Execution Checklist

- [ ] Step 1: Add `return` after `AbortWithStatus` in `authenticate` and `appAuthenticate` in `handlers.go`
- [ ] Step 2: Replace all `ioutil.ReadAll` with `io.ReadAll` and `ioutil.NopCloser` with `io.NopCloser` in `handlers.go` and `xtreamHandles.go`; remove `ioutil` imports
- [ ] Step 3: Add `Timeout: 30 * time.Second` to both `http.Client{}` instances in `handlers.go` and `xtreamHandles.go`
- [ ] Step 4: Use `PathEscape()` for User/Password in all route patterns in `routes.go`
- [ ] Step 5: Rewrite `xtreamGet` URL construction to use `url.Values` in `xtreamHandles.go`
- [ ] Step 6: Remove password from log line in `cmd/root.go`
- [ ] Step 7: Remove redundant outer `if` for CacheFolder in `cmd/root.go`
- [ ] Step 8: Add `skip` parameter to `formatError`; update callers to pass `2`
- [ ] Step 9: Cache `getErrorDetailLevel` at init; add `SetErrorDetailLevel`; update all test `os.Setenv` calls
- [ ] Step 10: Rename `ExampleErrorFormats` to `logErrorFormats` in `error_utils_test.go`
- [ ] Step 11: Add old file cleanup in `cacheXtreamM3u` in `xtreamHandles.go`
- [ ] Step 12: Fix cache lock pattern in `xtreamGet` and `xtreamApiGet`
- [ ] Step 13: Replace string-based type matching in `ProcessResponse` and `isXtreamCodesStruct`
- [ ] Step 14: Add empty-input guards to `FlexFloat.UnmarshalJSON` and `JSONStringSlice.UnmarshalJSON`
- [ ] Step 15: Forward upstream body in `hlsXtreamStream` non-302 path
- [ ] Step 16: Change Dockerfile to use `-mod=vendor` and remove `GO111MODULE=off`
- [ ] Step 17: Remove `utils` and `gin` imports from vendored `go.xtream-codes`; inline `logError` and `writeResponseCache`; update `vendor/modules.txt`
- [ ] Step 18: Create `pkg/server/handlers_test.go` with authentication tests
- [ ] Step 19: Add `TestProcessResponseEdgeCases` to `xtreamHandles_test.go`
- [ ] Step 20: Add `TestFormatErrorReportsCallerLocation` to `error_utils_test.go`
- [ ] Step 21: Update `vendor/modules.txt` to remove stale dependency references
- [ ] Run `go build ./...` -- must succeed with no errors
- [ ] Run `go vet ./...` -- must succeed with no errors
- [ ] Run `go test -v -count=1 -timeout 120s ./pkg/server/... ./pkg/utils/...` -- all tests pass
- [ ] Run `docker build -t iptv-proxy-test .` -- build succeeds

---

## Open Questions

None. All findings have clear, deterministic fixes based on the codebase as read. The one architectural concern (F9: global mutable state) is noted but intentionally scoped out of this plan as it would require a broader refactor beyond the review's scope.
