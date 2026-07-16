package handler

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *GatewayHandler) usageWalletBalance(c *gin.Context, ctx context.Context, apiKey *service.APIKey, userID int64) float64 {
	fallback := 0.0
	apiKeyID := int64(0)
	if apiKey != nil {
		apiKeyID = apiKey.ID
		if apiKey.User != nil && apiKey.User.ID == userID {
			fallback = apiKey.User.Balance
		}
	}
	if h.billingCacheService == nil {
		return fallback
	}

	balance, err := h.billingCacheService.GetUserBalance(ctx, userID)
	if err == nil {
		return balance
	}
	requestLogger(
		c,
		"handler.gateway.usage",
		zap.Int64("user_id", userID),
		zap.Int64("api_key_id", apiKeyID),
	).Warn("gateway.usage_balance_load_failed_using_auth_snapshot", zap.Error(err))
	return fallback
}
