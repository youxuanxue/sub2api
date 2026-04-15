package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// tkOpenAIChatSelectionCtx applies Tier1 affinity prefetch for OpenAI-compat chat completions selection.
func (h *OpenAIGatewayHandler) tkOpenAIChatSelectionCtx(c *gin.Context, apiKey *service.APIKey, reqModel string) (context.Context, string) {
	groupName := TkAPIKeyGroupName(apiKey)
	return h.withAffinityPrefetchedSession(c.Request.Context(), c, apiKey.GroupID, groupName, reqModel), groupName
}
