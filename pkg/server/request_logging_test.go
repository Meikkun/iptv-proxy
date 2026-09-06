package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pierre-emmanuelJ/iptv-proxy/pkg/config"
)

func TestActiveRequestLogsAndStatusDoNotExposeCredentials(t *testing.T) {
	if os.Getenv("IPTV_REQUEST_LOG_TEST_HELPER") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestActiveRequestLogsAndStatusDoNotExposeCredentials$")
		cmd.Env = append(os.Environ(), "IPTV_REQUEST_LOG_TEST_HELPER=1", "GIN_MODE=debug")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("debug-mode logging helper failed: %v\n%s", err, output)
		}
		for _, secret := range []string{"fixture-", "provider.example", "http://"} {
			if strings.Contains(string(output), secret) {
				t.Fatalf("credential log disclosure: %s", output)
			}
		}
		if !strings.Contains(string(output), "status=200") {
			t.Fatal("safe request log missing")
		}
		return
	}
	if gin.Mode() != gin.DebugMode {
		t.Fatal("logging regression must exercise actual debug-mode configuration")
	}
	config.DebugLoggingEnabled = true
	conf := catalogueConfig()
	conf.User, conf.Password = "fixture-user", "fixture-password"
	c, err := NewServer(conf)
	if err != nil {
		t.Fatal(err)
	}
	r := c.router()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/list.m3u?username=fixture-user&password=fixture-password&secret=fixture-query", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/unknown/fixture-path?secret=fixture-query", nil))
	redirect := httptest.NewRecorder()
	r.ServeHTTP(redirect, httptest.NewRequest("GET", "/list.m3u/?username=fixture-user&password=fixture-password&secret=fixture-query", nil))
	if redirect.Code != http.StatusMovedPermanently ||
		redirect.Header().Get("Location") != "/list.m3u?username=fixture-user&password=fixture-password&secret=fixture-query" {
		t.Fatalf("trailing-slash redirect changed: status=%d location=%q", redirect.Code, redirect.Header().Get("Location"))
	}

	oldClient := streamingHTTPClient
	defer func() { streamingHTTPClient = oldClient }()
	const raw = "http://fixture-info:fixture-basic@provider.example/live/fixture-up-user/fixture-up-pass/a.ts?secret=fixture-token"
	streamingHTTPClient = &http.Client{Transport: relayRoundTripper(func(req *http.Request) (*http.Response, error) {
		status := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(status)
		c.status(ctx)
		for _, secret := range []string{"fixture-", "provider.example", "http://"} {
			if strings.Contains(status.Body.String(), secret) {
				t.Errorf("status disclosed upstream identity: %s", status.Body.String())
			}
		}
		return nil, fmt.Errorf("wrapped: %w", &url.Error{Op: "Get", URL: raw, Err: errors.New("failed at " + raw)})
	})}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("GET", "/fixture-path?token=fixture-query", nil)
	upstream, _ := url.Parse(raw)
	c.stream(ctx, upstream)
	if strings.Contains(ctx.Errors.String(), "fixture-") {
		t.Errorf("wrapped HTTP error disclosed URL: %s", ctx.Errors.String())
	}
}
