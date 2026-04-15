package handler

import (
	"context"
	"strings"

	newapifusion "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

var (
	openAIGetPreferredAccountByAffinity = newapifusion.GetPreferredAccountByAffinity
	openAIMarkAffinitySelected          = newapifusion.MarkAffinitySelected
	openAIRecordAffinitySuccess         = newapifusion.RecordAffinitySuccess
)

func (h *OpenAIGatewayHandler) metadataBridgeEnabled() bool {
	if h == nil || h.cfg == nil {
		return true
	}
	return h.cfg.Gateway.OpenAIWS.MetadataBridgeEnabled
}

func (h *OpenAIGatewayHandler) withAffinityPrefetchedSession(
	ctx context.Context,
	c *gin.Context,
	groupID *int64,
	groupName string,
	reqModel string,
) context.Context {
	if c == nil || strings.TrimSpace(reqModel) == "" {
		return ctx
	}

	preferredID, ok := openAIGetPreferredAccountByAffinity(c, reqModel, groupName)
	if !ok || preferredID <= 0 {
		return ctx
	}

	prefetchedGroupID := int64(0)
	if groupID != nil {
		prefetchedGroupID = *groupID
	}
	return service.WithPrefetchedStickySession(ctx, preferredID, prefetchedGroupID, h.metadataBridgeEnabled())
}
