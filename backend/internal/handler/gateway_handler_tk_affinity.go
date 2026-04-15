package handler

import (
	"context"

	newapifusion "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

func tkAffinityPrefetchedGroupID(groupID *int64) int64 {
	if groupID != nil {
		return *groupID
	}
	return 0
}

// TkGatewayWithAffinityPrefetch applies new-api Tier1 affinity prefetch to a selection context when
// there is no session-bound sticky account (main gateway failover loops).
func TkGatewayWithAffinityPrefetch(c *gin.Context, h *GatewayHandler, selectionCtx context.Context, reqModel, groupName string, sessionBoundAccountID int64, groupID *int64) context.Context {
	if preferredID, ok := newapifusion.GetPreferredAccountByAffinity(c, reqModel, groupName); ok && sessionBoundAccountID <= 0 {
		return service.WithPrefetchedStickySession(selectionCtx, preferredID, tkAffinityPrefetchedGroupID(groupID), h.metadataBridgeEnabled())
	}
	return selectionCtx
}

// TkChatCompletionsWithAffinityPrefetch applies affinity prefetch for Anthropic-shaped chat completions routing.
func TkChatCompletionsWithAffinityPrefetch(c *gin.Context, h *GatewayHandler, selectionCtx context.Context, reqModel, groupName string, groupID *int64) context.Context {
	if preferredID, ok := newapifusion.GetPreferredAccountByAffinity(c, reqModel, groupName); ok {
		return service.WithPrefetchedStickySession(selectionCtx, preferredID, tkAffinityPrefetchedGroupID(groupID), h.metadataBridgeEnabled())
	}
	return selectionCtx
}

// TkGatewayApplyAffinityToRequest mutates the gin request context when affinity matches (Gemini v1beta entry).
func TkGatewayApplyAffinityToRequest(c *gin.Context, h *GatewayHandler, modelName, groupName string, sessionBoundAccountID int64, groupID *int64) {
	if sessionBoundAccountID > 0 {
		return
	}
	if preferredID, ok := newapifusion.GetPreferredAccountByAffinity(c, modelName, groupName); ok && preferredID > 0 {
		ctx := service.WithPrefetchedStickySession(c.Request.Context(), preferredID, tkAffinityPrefetchedGroupID(groupID), h.metadataBridgeEnabled())
		c.Request = c.Request.WithContext(ctx)
	}
}

// TkGatewayMarkAffinitySelected records affinity selection for observability.
func TkGatewayMarkAffinitySelected(c *gin.Context, groupName string, accountID int64) {
	newapifusion.MarkAffinitySelected(c, groupName, accountID)
}

// TkGatewayRecordAffinitySuccess records a successful forward for affinity stickiness.
func TkGatewayRecordAffinitySuccess(c *gin.Context, accountID int64) {
	newapifusion.RecordAffinitySuccess(c, accountID)
}
