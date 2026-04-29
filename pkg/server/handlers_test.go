package server

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
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
