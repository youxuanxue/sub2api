package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// tkAnthropicFailoverExhaustedDefaultResponse maps a generic upstream failover error
// to Anthropic client format with TokenKey forbidden/incident message enrichment.
func (h *OpenAIGatewayHandler) tkAnthropicFailoverExhaustedDefaultResponse(c *gin.Context, failoverErr *service.UpstreamFailoverError, streamStarted bool) {
	if failoverErr == nil {
		h.anthropicStreamingAwareError(c, http.StatusBadGateway, "api_error", "Upstream request failed", streamStarted)
		return
	}
	status, errType, errMsg := h.mapUpstreamError(failoverErr.StatusCode)
	if msg := service.ExtractUpstreamErrorMessage(failoverErr.ResponseBody); msg != "" {
		errMsg = msg
	}
	if failoverErr.StatusCode == http.StatusForbidden {
		errMsg = service.TkEnrichForbiddenMessage(c, errMsg)
	}
	errMsg = service.TkEnrichClaudeIncidentMessage(errMsg, failoverErr.StatusCode)
	h.anthropicStreamingAwareError(c, status, errType, errMsg, streamStarted)
}
