package service

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
)

// tkGeminiNonStreamUpstreamContext detaches non-stream Gemini upstream attempts
// from the caller's cancellation. Google bills a generateContent call once it
// is accepted; cancelling the HTTP request on client disconnect only throws the
// usage away (Wei-Shaw/sub2api#7200). It reuses the shared drain owner
// (detachUpstreamContext → pkg/httputil.DetachUpstreamContext): the transport
// starts the bounded drain budget (gateway.stream_data_interval_timeout,
// default 180s) when the client leaves, so a hung upstream cannot pin the
// attempt forever. Streams keep their armed first-output context; skip=true
// (stream / countTokens) returns ctx unchanged.
func tkGeminiNonStreamUpstreamContext(ctx context.Context, skip bool) (context.Context, context.CancelFunc) {
	if skip {
		return ctx, func() {}
	}
	return detachUpstreamContext(ctx)
}

// tkGeminiCallerGoneBeforeRetry stops same-account retries once the caller is
// gone: the detached attempt in flight is drained for billing, but a new
// attempt would be pure upstream cost nobody receives. The first attempt is
// never blocked here; the handler's pre-forward checks own that.
func tkGeminiCallerGoneBeforeRetry(callerCtx context.Context, attempt int) error {
	if attempt <= 1 || callerCtx == nil {
		return nil
	}
	return callerCtx.Err()
}

// tkGeminiMarkClientGoneAfterNonStream records the client-closed-499 SSOT
// marker when a detached non-stream attempt completed after the caller left.
// The usage still settles normally; the marker keeps ops from attributing the
// undelivered response to the platform.
func tkGeminiMarkClientGoneAfterNonStream(c *gin.Context, callerCtx context.Context, stream bool) {
	if stream || c == nil || callerCtx == nil || !errors.Is(callerCtx.Err(), context.Canceled) {
		return
	}
	MarkOpsClientClosedRequest(c)
	if c.Writer != nil && !c.Writer.Written() {
		c.Status(StatusClientClosedRequest)
	}
}
