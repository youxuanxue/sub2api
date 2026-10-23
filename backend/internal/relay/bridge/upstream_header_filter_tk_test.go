//go:build unit

package bridge

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUpstreamHeaderFilterDropsSupplierHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Header("X-Request-Id", "tk-req")
	c.Header("X-Client-Request-Id", "client-req")
	c.Header("X-Frame-Options", "DENY")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
	c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")

	restore := filterUpstreamResponseHeaders(c)
	defer restore()

	c.Writer.Header().Set("X-Request-Id", "upstream-req")
	c.Writer.Header().Set("X-Dashscope-Providerdeployid", "dash-kimi-k3-v0.4")
	c.Writer.Header().Set("X-Dashscope-Call-Gateway", "true")
	c.Writer.Header().Set("Server", "istio-envoy")
	c.Writer.Header().Set("X-Volc-Request-Id", "volc-1")
	c.Writer.Header().Set("X-Tt-Logid", "tt-1")
	c.Writer.Header().Set("Req-Arrive-Time", "1")
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Header().Set("Retry-After", "2")
	c.Writer.Header().Set("Content-Length", "99")
	if _, err := c.Writer.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	h := recorder.Header()
	if got := h.Get("X-Request-Id"); got != "tk-req" {
		t.Fatalf("X-Request-Id = %q, want gateway id", got)
	}
	if got := h.Get("X-Client-Request-Id"); got != "client-req" {
		t.Fatalf("X-Client-Request-Id = %q", got)
	}
	if h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers changed: %v", h)
	}
	if h.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", h.Get("Content-Type"))
	}
	if h.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("X-Accel-Buffering = %q", h.Get("X-Accel-Buffering"))
	}
	if h.Get("Retry-After") != "2" {
		t.Fatalf("Retry-After = %q", h.Get("Retry-After"))
	}
	for _, key := range []string{
		"X-Dashscope-Providerdeployid",
		"X-Dashscope-Call-Gateway",
		"Server",
		"X-Volc-Request-Id",
		"X-Tt-Logid",
		"Req-Arrive-Time",
	} {
		if h.Get(key) != "" {
			t.Fatalf("%s leaked: %q", key, h.Get(key))
		}
	}
	if h.Get("Content-Length") == "99" {
		t.Fatal("upstream Content-Length leaked")
	}
}

func TestUpstreamHeaderFilterStripsOnFlush(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Header("X-Request-Id", "tk-req")

	restore := filterUpstreamResponseHeaders(c)
	defer restore()

	c.Writer.Header().Set("X-Request-Id", "upstream-req")
	c.Writer.Header().Set("X-Dashscope-Providerdeployid", "dash-kimi-k3-v0.4")
	c.Writer.Header().Set("Server", "istio-envoy")
	c.Writer.Flush()

	h := recorder.Header()
	if got := h.Get("X-Request-Id"); got != "tk-req" {
		t.Fatalf("X-Request-Id = %q, want gateway id", got)
	}
	if h.Get("X-Dashscope-Providerdeployid") != "" || h.Get("Server") != "" {
		t.Fatalf("supplier headers leaked on flush: %v", h)
	}
}
