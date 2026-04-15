package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// TkAPIKeyGroupName returns the API key group's display name, or empty when absent.
func TkAPIKeyGroupName(key *service.APIKey) string {
	if key == nil || key.Group == nil {
		return ""
	}
	return key.Group.Name
}

// TkGatewayMessagesAffinitySelectionCtx builds selection context for /v1/messages failover loops.
func TkGatewayMessagesAffinitySelectionCtx(c *gin.Context, h *GatewayHandler, apiKey *service.APIKey, reqModel string, sessionBoundAccountID int64) (context.Context, string) {
	groupName := TkAPIKeyGroupName(apiKey)
	selectionCtx := TkGatewayWithAffinityPrefetch(c, h, c.Request.Context(), reqModel, groupName, sessionBoundAccountID, apiKey.GroupID)
	return selectionCtx, groupName
}

// TkGatewayAnthropicCompatAffinitySelectionCtx builds selection context for Anthropic-shaped gateway
// chat completions / responses routes (OpenAI-compat conversion path).
func TkGatewayAnthropicCompatAffinitySelectionCtx(c *gin.Context, h *GatewayHandler, apiKey *service.APIKey, reqModel string) (context.Context, string) {
	groupName := TkAPIKeyGroupName(apiKey)
	selectionCtx := TkChatCompletionsWithAffinityPrefetch(c, h, c.Request.Context(), reqModel, groupName, apiKey.GroupID)
	return selectionCtx, groupName
}
