package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestReplaceTrackPathBasePreservesQuery(t *testing.T) {
	rawURL, err := replaceTrackPathBase("http://provider.example/live/master.m3u8?token=abc", "chunk.ts")
	if err != nil {
		t.Fatalf("replaceTrackPathBase() error = %v", err)
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	if parsedURL.Path != "/live/chunk.ts" {
		t.Fatalf("replaceTrackPathBase() path = %q, want %q", parsedURL.Path, "/live/chunk.ts")
	}

	if parsedURL.RawQuery != "token=abc" {
		t.Fatalf("replaceTrackPathBase() query = %q, want %q", parsedURL.RawQuery, "token=abc")
	}
}

func TestReadLimitedRequestBodyTooLarge(t *testing.T) {
	_, err := readLimitedRequestBody(io.NopCloser(strings.NewReader(strings.Repeat("x", 9))), 8)
	if !errors.Is(err, errRequestBodyTooLarge) {
		t.Fatalf("readLimitedRequestBody() error = %v, want errRequestBodyTooLarge", err)
	}
}

func TestReadLimitedRequestBodyWithinLimit(t *testing.T) {
	body, err := readLimitedRequestBody(io.NopCloser(strings.NewReader("abcd")), 8)
	if err != nil {
		t.Fatalf("readLimitedRequestBody() error = %v", err)
	}

	if string(body) != "abcd" {
		t.Fatalf("readLimitedRequestBody() body = %q, want %q", string(body), "abcd")
	}
}

func TestNormalizeHTTPStatusForError(t *testing.T) {
	err := errors.New("boom")

	if got := normalizeHTTPStatusForError(0, err); got != http.StatusInternalServerError {
		t.Fatalf("normalizeHTTPStatusForError() = %d, want %d", got, http.StatusInternalServerError)
	}

	if got := normalizeHTTPStatusForError(http.StatusBadRequest, err); got != http.StatusBadRequest {
		t.Fatalf("normalizeHTTPStatusForError() = %d, want %d", got, http.StatusBadRequest)
	}

	if got := normalizeHTTPStatusForError(http.StatusCreated, nil); got != http.StatusCreated {
		t.Fatalf("normalizeHTTPStatusForError() = %d, want %d", got, http.StatusCreated)
	}
}

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

func TestMergeHttpHeaderPreservesDuplicateSkip(t *testing.T) {
	dst := http.Header{"Accept": {"text/html"}}
	src := http.Header{"Accept": {"text/html", "application/json"}}

	mergeHttpHeader(dst, src)

	got := dst.Values("Accept")
	if len(got) != 2 {
		t.Fatalf("Expected 2 Accept values, got %d: %v", len(got), got)
	}
}

func TestStreamHandlesUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)

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
