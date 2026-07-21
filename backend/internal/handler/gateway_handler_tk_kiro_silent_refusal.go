package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *GatewayHandler) handleKiroSilentRefusalMessages(c *gin.Context, err error, streamStarted bool) bool {
	var silentRefusalErr *service.KiroSilentRefusalError
	if !errors.As(err, &silentRefusalErr) {
		return false
	}
	c.Header(service.KiroOutcomeHeader, service.KiroSilentRefusalOutcome)
	h.handleStreamingAwareError(
		c,
		http.StatusBadGateway,
		"upstream_error",
		service.KiroSilentRefusalClientMessage(),
		streamStarted,
	)
	return true
}

func (h *GatewayHandler) handleKiroSilentRefusalChatCompletions(c *gin.Context, err error) bool {
	var silentRefusalErr *service.KiroSilentRefusalError
	if !errors.As(err, &silentRefusalErr) {
		return false
	}
	c.Header(service.KiroOutcomeHeader, service.KiroSilentRefusalOutcome)
	h.chatCompletionsErrorResponse(c, http.StatusBadGateway, "server_error", "All available accounts exhausted")
	return true
}
