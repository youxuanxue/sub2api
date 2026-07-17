package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// tkWriteUnsupportedModelIfApplicable maps a service.ErrUnsupportedModel account
// selection failure to an HTTP 400 invalid_request_error and returns true
// (handled). For any other error it returns false so the caller falls through to
// the original "No available accounts" (429) path.
//
// Why this is a 400, not a 429 (prod incident 2026-06-06, user_id=16): the
// scheduler determined that NO account in the pool serves the requested model
// NAME (e.g. the bare alias "opus" instead of "claude-opus-4-8"). That is a
// caller error — the client asked for a model nobody serves — not a provider
// rate limit and not a transient capacity gap. Surfacing it as 400
// invalid_request_error makes ops classify it as a client-owned request error
// (phase=request, P3) instead of a misleading "Anthropic rate limit", and stops
// the client from retry-storming a request that can never succeed as sent.
//
// It deliberately does NOT call markOpsRoutingCapacityLimitedIfNoAvailable: this
// is not a routing-capacity event.
func (h *GatewayHandler) tkWriteUnsupportedModelIfApplicable(c *gin.Context, err error, reqModel string, streamStarted bool, reqLog *zap.Logger) bool {
	if !errors.Is(err, service.ErrUnsupportedModel) {
		return false
	}
	if reqLog != nil {
		reqLog.Warn("gateway.select_account_unsupported_model",
			zap.String("model", reqModel),
			zap.Error(err),
		)
	}
	// Own this to the client in ops regardless of the response envelope, so a
	// client-fault model name never reads as TK capacity/internal.
	markOpsClientRequestRejected(c)
	h.handleStreamingAwareError(c, http.StatusBadRequest, service.TkUnsupportedModelErrType, service.TkUnsupportedModelMessage(reqModel), streamStarted)
	return true
}

// tkWriteUnsupportedAnthropicModelAtIngress rejects non-claude-* model names on
// Anthropic platform routes before account selection so cross-vendor dirty names
// (model=gpt, bare opus, deepseek-*, …) never enter load-batch scheduling or
// surface as empty-pool routing 429.
func (h *GatewayHandler) tkWriteUnsupportedAnthropicModelAtIngress(c *gin.Context, reqModel string, streamStarted bool, reqLog *zap.Logger) bool {
	if !service.TkIsAnthropicCrossVendorModelName(reqModel) {
		return false
	}
	if reqLog != nil {
		reqLog.Warn("gateway.unsupported_model_ingress_reject", zap.String("model", reqModel))
	}
	return h.tkWriteUnsupportedModelIfApplicable(c, fmt.Errorf("%w: %s (anthropic namespace)", service.ErrUnsupportedModel, reqModel), reqModel, streamStarted, reqLog)
}
