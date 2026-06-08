package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newKeepaliveTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c, rec
}

// A slow upstream (one that has not returned headers within a keepalive
// interval) must produce SSE ping frames and the SSE response headers, so an
// idle-sensitive intermediary does not drop the connection (Wei-Shaw#2121).
func TestStartHeaderWaitKeepalive_EmitsPingsAfterInterval(t *testing.T) {
	c, rec := newKeepaliveTestContext(t)

	k := startHeaderWaitKeepalive(c, 20*time.Millisecond, anthropicSSEPingFrame)
	if k == nil {
		t.Fatal("expected non-nil keepalive handle for positive interval")
	}
	// Simulate an upstream that blocks past several intervals before stop().
	time.Sleep(90 * time.Millisecond)
	k.stop()

	body := rec.Body.String()
	if got := strings.Count(body, "event: ping"); got < 1 {
		t.Fatalf("expected at least one ping frame, got %d (body=%q)", got, body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected SSE content-type committed before first ping, got %q", ct)
	}
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("expected X-Accel-Buffering=no, got %q", rec.Header().Get("X-Accel-Buffering"))
	}
	if !c.Writer.Written() {
		t.Fatal("expected writer marked written after pings")
	}
}

// The failover-safety contract: if the upstream returns BEFORE one keepalive
// interval elapses (the path that every fast 429/503/5xx failover takes), NOT a
// single byte may be written — otherwise the handler's c.Writer.Written()
// failover gate would be tripped and transparent failover lost.
func TestStartHeaderWaitKeepalive_NoWriteBeforeInterval(t *testing.T) {
	c, rec := newKeepaliveTestContext(t)

	k := startHeaderWaitKeepalive(c, 500*time.Millisecond, anthropicSSEPingFrame)
	if k == nil {
		t.Fatal("expected non-nil keepalive handle")
	}
	// Upstream "returned" almost immediately, well within the interval.
	time.Sleep(5 * time.Millisecond)
	k.stop()

	if rec.Body.Len() != 0 {
		t.Fatalf("expected no body written before interval, got %q", rec.Body.String())
	}
	if c.Writer.Written() {
		t.Fatal("writer must not be marked written before any ping (failover gate)")
	}
}

func TestStartHeaderWaitKeepalive_DisabledReturnsNil(t *testing.T) {
	c, _ := newKeepaliveTestContext(t)
	if k := startHeaderWaitKeepalive(c, 0, anthropicSSEPingFrame); k != nil {
		t.Fatal("expected nil handle when interval <= 0")
	}
	// stop() on a nil handle must be a safe no-op.
	var nilHandle *headerWaitKeepalive
	nilHandle.stop()
}

func TestBeginHeaderWaitKeepalive_NonStreamReturnsNil(t *testing.T) {
	c, _ := newKeepaliveTestContext(t)
	// reqStream=false short-circuits before any config lookup, so a zero-value
	// service is sufficient.
	if k := (&GatewayService{}).beginHeaderWaitKeepalive(c, false); k != nil {
		t.Fatal("expected nil handle for non-streaming request")
	}
	// Same contract for the OpenAI passthrough analogue.
	if k := (&OpenAIGatewayService{}).beginHeaderWaitKeepalive(c, false); k != nil {
		t.Fatal("expected nil handle for non-streaming OpenAI request")
	}
}

// The OpenAI/Codex passthrough heartbeat must be a bare SSE comment (":\n\n"),
// never a typed event frame — the strict Codex/Responses SDK rejects unknown
// events. This pins the frame and the same pre-headers behavior.
func TestStartHeaderWaitKeepalive_OpenAICommentFrame(t *testing.T) {
	c, rec := newKeepaliveTestContext(t)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	k := startHeaderWaitKeepalive(c, 20*time.Millisecond, openaiSSECommentFrame)
	if k == nil {
		t.Fatal("expected non-nil keepalive handle for positive interval")
	}
	time.Sleep(90 * time.Millisecond)
	k.stop()

	body := rec.Body.String()
	if !strings.Contains(body, ":\n\n") {
		t.Fatalf("expected SSE comment keepalive, got %q", body)
	}
	if strings.Contains(body, "event:") {
		t.Fatalf("OpenAI keepalive must not emit a typed event frame, got %q", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected SSE content-type committed before first comment, got %q", ct)
	}
}
