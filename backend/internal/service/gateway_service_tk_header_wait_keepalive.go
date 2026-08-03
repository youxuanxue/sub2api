package service

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// anthropicSSEPingFrame is the Anthropic-shaped SSE keepalive frame. It is byte
// identical to the in-stream keepalive emitted by handleStreamingResponse* so a
// downstream client sees exactly one ping event type whether the heartbeat was
// produced before or after the upstream started streaming.
const anthropicSSEPingFrame = "event: ping\ndata: {\"type\": \"ping\"}\n\n"

// openaiSSECommentFrame is the OpenAI/Codex-shaped SSE keepalive frame. The
// OpenAI passthrough in-stream keepalive uses a bare SSE comment line (":\n\n"),
// which every SSE client ignores — unlike the Anthropic path it must NOT emit a
// typed event, because the strict Codex/Responses SDK rejects unknown event
// frames. Kept byte identical to that in-stream frame for the same reason as
// anthropicSSEPingFrame.
const openaiSSECommentFrame = ":\n\n"

// headerWaitKeepalive emits SSE ping frames to the downstream client while the
// upstream is still being dialed / has not yet returned its response headers.
// See beginHeaderWaitKeepalive for the failover-safety rationale.
type headerWaitKeepalive struct {
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

// stop terminates the keepalive goroutine and blocks until it has fully
// returned, guaranteeing that no ping can race a subsequent write to the same
// gin.ResponseWriter. It is safe to call on a nil receiver (the no-op case
// returned by beginHeaderWaitKeepalive when keepalive is disabled).
func (k *headerWaitKeepalive) stop() {
	if k == nil {
		return
	}
	k.once.Do(func() { close(k.stopCh) })
	<-k.doneCh
}

// beginHeaderWaitKeepalive starts a background SSE ping emitter for streaming
// Anthropic requests. It is a no-op (returns nil) for non-streaming requests or
// when keepalive is disabled (gateway.stream_keepalive_interval <= 0). The
// returned handle MUST be stopped (k.stop()) the instant the upstream call
// returns, before any other code writes to c.Writer.
//
// Why this exists (Wei-Shaw/sub2api#2121): the existing in-stream keepalive only
// fires AFTER the upstream has produced its first SSE byte. While Forward blocks
// in DoWithTLS waiting for the upstream response headers — which can take a long
// time when the upstream is queueing under load or "thinking" before the first
// token — the downstream connection is completely idle. Idle-sensitive
// intermediaries (Cloudflare Tunnel, Caddy, client SDKs) then drop the
// connection and a request that would have succeeded is lost. This is amplified
// on the prod->edge mirror-relay topology, where the same header-wait window is
// traversed on two hops.
//
// Failover safety: the FIRST ping is delayed by one full keepalive interval, so
// any failover-eligible upstream outcome (429/503/5xx, empty-pool fast-fail,
// fast network refusal) — all of which return within ~1-2s — is decided before
// a single byte is written. The handler's c.Writer.Written() failover gate is
// therefore preserved for the overwhelming majority of requests. Only an
// upstream that stays silent past one full interval and THEN fails loses
// transparent failover; that request is already pathologically slow, and the
// pre-existing ensureForwardErrorResponse path still delivers a protocol-
// compliant SSE terminal event to the downstream client.
func (s *GatewayService) beginHeaderWaitKeepalive(c *gin.Context, reqStream bool) *headerWaitKeepalive {
	if s == nil || s.cfg == nil {
		return nil
	}
	return beginConfiguredHeaderWaitKeepalive(c, reqStream, s.cfg.Gateway.StreamKeepaliveInterval, anthropicSSEPingFrame)
}

// beginHeaderWaitKeepalive (OpenAI) is the OpenAI/Codex passthrough analogue of
// the Anthropic header-wait keepalive above. The OpenAI passthrough streaming
// forward (forwardOpenAIPassthrough) blocks in s.httpUpstream.Do(...) waiting for
// the upstream response headers exactly like the Anthropic Forward path, with the
// same idle-drop exposure and the same failover gate (the handler compares
// c.Writer.Size() before/after forward). Reuses the same config knob and the same
// failover-safe delayed-first-ping construction; it differs only in the emitted
// frame — a bare SSE comment that the strict Codex/Responses SDK tolerates. A
// no-op for non-streaming/disabled requests.
func (s *OpenAIGatewayService) beginHeaderWaitKeepalive(c *gin.Context, reqStream bool) *headerWaitKeepalive {
	if s == nil || s.cfg == nil {
		return nil
	}
	return beginConfiguredHeaderWaitKeepalive(c, reqStream, s.cfg.Gateway.StreamKeepaliveInterval, openaiSSECommentFrame)
}

// beginConfiguredHeaderWaitKeepalive is the shared constructor used by every
// streaming platform Forward/Do path (Anthropic, OpenAI, Kiro, Bedrock,
// Antigravity, Gemini). intervalSec comes from gateway.stream_keepalive_interval.
func beginConfiguredHeaderWaitKeepalive(c *gin.Context, reqStream bool, intervalSec int, frame string) *headerWaitKeepalive {
	if !reqStream || intervalSec <= 0 {
		return nil
	}
	return startHeaderWaitKeepalive(c, time.Duration(intervalSec)*time.Second, frame)
}

func (s *AntigravityGatewayService) beginHeaderWaitKeepalive(c *gin.Context, reqStream bool) *headerWaitKeepalive {
	if s == nil || s.settingService == nil || s.settingService.cfg == nil {
		return nil
	}
	return beginConfiguredHeaderWaitKeepalive(c, reqStream, s.settingService.cfg.Gateway.StreamKeepaliveInterval, anthropicSSEPingFrame)
}

func (s *GeminiMessagesCompatService) beginHeaderWaitKeepalive(c *gin.Context, reqStream bool) *headerWaitKeepalive {
	if s == nil || s.cfg == nil {
		return nil
	}
	return beginConfiguredHeaderWaitKeepalive(c, reqStream, s.cfg.Gateway.StreamKeepaliveInterval, anthropicSSEPingFrame)
}

// beginSSECommentHeaderWaitKeepalive emits the wire-neutral SSE comment keepalive
// for OpenAI Responses/Chat Completions compat and Google-native stream passthrough.
// Strict OpenAI SDKs reject Anthropic-shaped ping events on those ingresses.
func (s *GeminiMessagesCompatService) beginSSECommentHeaderWaitKeepalive(c *gin.Context, reqStream bool) *headerWaitKeepalive {
	if s == nil || s.cfg == nil {
		return nil
	}
	return beginConfiguredHeaderWaitKeepalive(c, reqStream, s.cfg.Gateway.StreamKeepaliveInterval, openaiSSECommentFrame)
}

// startHeaderWaitKeepalive is the interval-driven core of beginHeaderWaitKeepalive,
// split out so tests can drive it with a sub-second interval. frame is the SSE
// bytes emitted each tick (platform specific). interval <= 0, a nil
// context/writer, or a writer that cannot flush all yield a nil no-op.
func startHeaderWaitKeepalive(c *gin.Context, interval time.Duration, frame string) *headerWaitKeepalive {
	if interval <= 0 || c == nil || c.Writer == nil || c.Request == nil {
		return nil
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil
	}

	k := &headerWaitKeepalive{
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go func() {
		defer close(k.doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		ctxDone := c.Request.Context().Done()
		headersSent := false
		for {
			select {
			case <-k.stopCh:
				return
			case <-ctxDone:
				return
			case <-ticker.C:
				if !headersSent {
					// Commit SSE response headers exactly once, before the first
					// ping forces WriteHeader(200). Mirrors the header set in
					// handleStreamingResponseAnthropicAPIKeyPassthrough so the
					// real stream handler's later c.Header() calls are harmless
					// no-ops on the already-sent header block.
					h := c.Writer.Header()
					if h.Get("Content-Type") == "" {
						h.Set("Content-Type", "text/event-stream")
					}
					if h.Get("Cache-Control") == "" {
						h.Set("Cache-Control", "no-cache")
					}
					if h.Get("Connection") == "" {
						h.Set("Connection", "keep-alive")
					}
					h.Set("X-Accel-Buffering", "no")
					headersSent = true
				}
				if _, err := fmt.Fprint(c.Writer, frame); err != nil {
					// Client is gone; stop emitting. The upstream read path will
					// observe the disconnect via context cancellation or its own
					// write failure once it takes over.
					return
				}
				flusher.Flush()
			}
		}
	}()
	return k
}

// preContentStreamKeepaliveKey stores a header-wait / pre-content keepalive on
// the gin context so nested streaming writers (Kiro thinking stall, Bedrock Do,
// etc.) can stop it the instant the first client-visible SSE byte is written,
// without racing the keepalive goroutine.
const preContentStreamKeepaliveKey = "tk_pre_content_stream_keepalive"

// bindPreContentStreamKeepalive attaches a keepalive handle for later stop from
// the first client-visible write path. No-op when c or k is nil.
func bindPreContentStreamKeepalive(c *gin.Context, k *headerWaitKeepalive) {
	if c == nil || k == nil {
		return
	}
	c.Set(preContentStreamKeepaliveKey, k)
}

// stopPreContentStreamKeepalive stops any bound pre-content keepalive and
// clears the context slot. Safe to call repeatedly or when unbound.
func stopPreContentStreamKeepalive(c *gin.Context) {
	if c == nil {
		return
	}
	v, ok := c.Get(preContentStreamKeepaliveKey)
	if !ok || v == nil {
		return
	}
	if k, ok := v.(*headerWaitKeepalive); ok {
		k.stop()
	}
	c.Set(preContentStreamKeepaliveKey, nil)
}
