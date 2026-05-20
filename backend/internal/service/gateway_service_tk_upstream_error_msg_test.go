package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newTestGinCtxForUpstreamMsg() *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return c
}

func TestTkEnrichForbiddenMessage_NilContext(t *testing.T) {
	got := TkEnrichForbiddenMessage(nil, "Upstream access forbidden, please contact administrator")
	if got != "Upstream access forbidden, please contact administrator" {
		t.Fatalf("nil ctx must return defaultMsg verbatim, got %q", got)
	}
}

func TestTkEnrichForbiddenMessage_EmptyContext(t *testing.T) {
	c := newTestGinCtxForUpstreamMsg()
	got := TkEnrichForbiddenMessage(c, "default")
	if got != "default" {
		t.Fatalf("empty ctx must return defaultMsg, got %q", got)
	}
}

func TestTkEnrichForbiddenMessage_BodyAndModel(t *testing.T) {
	c := newTestGinCtxForUpstreamMsg()
	c.Set(OpsModelKey, "claude-opus-4-7")
	c.Set(OpsRequestBodyKey, make([]byte, 1234567))

	got := TkEnrichForbiddenMessage(c, "default")
	if !strings.Contains(got, "1234567") {
		t.Fatalf("expected body bytes 1234567 in msg, got %q", got)
	}
	if !strings.Contains(got, "claude-opus-4-7") {
		t.Fatalf("expected model in msg, got %q", got)
	}
	if !strings.Contains(got, "/compact") {
		t.Fatalf("expected /compact hint in msg, got %q", got)
	}
}

func TestTkEnrichForbiddenMessage_BodyOnly(t *testing.T) {
	c := newTestGinCtxForUpstreamMsg()
	c.Set(OpsRequestBodyKey, make([]byte, 5000))
	got := TkEnrichForbiddenMessage(c, "default")
	if !strings.Contains(got, "5000") {
		t.Fatalf("expected body bytes in msg, got %q", got)
	}
}

func TestTkEnrichForbiddenMessage_ModelOnly(t *testing.T) {
	c := newTestGinCtxForUpstreamMsg()
	c.Set(OpsModelKey, "claude-opus-4-7")
	got := TkEnrichForbiddenMessage(c, "default")
	if !strings.Contains(got, "claude-opus-4-7") {
		t.Fatalf("expected model in msg, got %q", got)
	}
}

func TestTkEnrichForbiddenMessage_EmptyBodyKeyButPresent(t *testing.T) {
	c := newTestGinCtxForUpstreamMsg()
	c.Set(OpsRequestBodyKey, []byte{})
	got := TkEnrichForbiddenMessage(c, "default")
	if got != "default" {
		t.Fatalf("zero-length body must keep defaultMsg, got %q", got)
	}
}
