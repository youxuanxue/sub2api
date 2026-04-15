package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) tkResponsesSelectionCtx(c *gin.Context, apiKey *service.APIKey, previousResponseID, reqModel string) (context.Context, string) {
	groupName := TkAPIKeyGroupName(apiKey)
	selectionCtx := c.Request.Context()
	if previousResponseID == "" {
		selectionCtx = h.withAffinityPrefetchedSession(selectionCtx, c, apiKey.GroupID, groupName, reqModel)
	}
	return selectionCtx, groupName
}

// tkTryHandleResponsesNewAPIRelayError handles new-api bridge dispatch errors for the Responses entrypoint.
// Returns true when the error was handled and the handler should return.
func (h *OpenAIGatewayHandler) tkTryHandleResponsesNewAPIRelayError(
	c *gin.Context,
	err error,
	account *service.Account,
	streamStarted bool,
	writerSizeBeforeForward int,
	reqLog *zap.Logger,
) bool {
	if !TkTryWriteNewAPIRelayErrorJSON(c, err, streamStarted, writerSizeBeforeForward) {
		return false
	}
	h.gatewayService.ReportOpenAIAccountScheduleResult(account.ID, false, nil)
	reqLog.Warn("openai.forward_failed",
		zap.String("component", "handler.openai_gateway.responses"),
		zap.Int64("account_id", account.ID),
		zap.Bool("fallback_error_response_written", false),
		zap.Error(err),
	)
	return true
}
