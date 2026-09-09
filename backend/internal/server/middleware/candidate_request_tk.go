package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func requestLocalCandidateKey(c *gin.Context, key *service.APIKey) *service.APIKey {
	copyKey := *key
	if key.User != nil {
		user := *key.User
		copyKey.User = &user
	}
	if key.GroupID != nil {
		groupID := *key.GroupID
		copyKey.GroupID = &groupID
	}
	if key.Group != nil {
		group := *key.Group
		copyKey.Group = &group
	}
	ctx := service.WithCandidateIdentity(c.Request.Context(), key.UserID, key.ID)
	ctx = context.WithValue(ctx, ctxkey.UserID, key.UserID)
	if key.IsUniversal() {
		ctx = service.WithUniversalKeyRouting(ctx)
	}
	c.Request = c.Request.WithContext(ctx)
	return &copyKey
}

func deferUniversalWebSocketBilling(c *gin.Context, key *service.APIKey) bool {
	if key == nil || !key.IsUniversal() || !isWebSocketUpgradeRequest(c) || c.Request.Method != http.MethodGet {
		return false
	}
	path := strings.TrimRight(c.Request.URL.Path, "/")
	return strings.HasSuffix(path, "/responses")
}

func observeCandidateBinding(c *gin.Context, state *service.CandidateRequest) {
	state.SetBindingObserver(func(ctx context.Context, group *service.Group, subscription *service.UserSubscription) {
		c.Request = c.Request.WithContext(ctx)
		setGroupContext(c, group)
		if subscription == nil {
			c.Set(string(ContextKeySubscription), nil)
		} else {
			c.Set(string(ContextKeySubscription), subscription)
		}
	})
}

func writeCandidateContinuationError(c *gin.Context, shape service.UniversalShape) {
	const message = "previous_response_id is not available for this user"
	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, http.StatusBadRequest, message)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		AnthropicErrorWriter(c, http.StatusBadRequest, message)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": message}})
	}
}

func writeCandidateBillingError(c *gin.Context, shape service.UniversalShape, err error) {
	status, message := infraerrors.Code(err), infraerrors.Message(err)
	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, status, message)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		AnthropicErrorWriter(c, status, message)
	default:
		c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "code": infraerrors.Reason(err), "message": message}})
	}
}
