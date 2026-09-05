package handler

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

func (h *GatewayHandler) tkLoadUsageAggregates(
	c *gin.Context,
	ctx context.Context,
	userID, apiKeyID int64,
	days int,
	startTime, endTime time.Time,
) (usageData gin.H, dailyUsage any, modelStats any) {
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { usageData = h.buildUsageData(gctx, apiKeyID); return nil })
	g.Go(func() error { dailyUsage = h.buildAPIKeyDailyUsage(c, userID, apiKeyID, days); return nil })
	g.Go(func() error {
		if h.usageService != nil {
			if stats, err := h.usageService.GetAPIKeyModelStats(gctx, apiKeyID, startTime, endTime); err == nil && len(stats) > 0 {
				modelStats = stats
			}
		}
		return nil
	})
	_ = g.Wait()
	return usageData, dailyUsage, modelStats
}

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

type tkClaudeGatewayForwardUsageInput struct {
	C                  *gin.Context
	APIKey             *service.APIKey
	Account            *service.Account
	Subscription       *service.UserSubscription
	Result             *service.ForwardResult
	ReqModel           string
	Body               []byte
	PricingAt          time.Time
	ChannelUsageFields service.ChannelUsageFields
	ReqLog             *zap.Logger
	LogFailedEvent     string
}

func (h *GatewayHandler) tkSubmitClaudeGatewayForwardUsage(in tkClaudeGatewayForwardUsageInput) {
	if in.Result == nil {
		return
	}
	userAgent := in.C.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(in.C)
	requestPayloadHash := service.HashUsageRequestPayload(in.Body)
	inboundEndpoint := GetInboundEndpoint(in.C)
	upstreamEndpoint := GetUpstreamEndpoint(in.C, in.Account.Platform)
	if facts, ok := in.Result.ProtocolRouteFacts(); ok {
		upstreamEndpoint = facts.UpstreamEndpoint()
	}
	quotaPlatform := service.QuotaPlatform(in.C.Request.Context(), in.APIKey)
	sessionID := service.ExtractClientSessionID(in.C)
	gatewayLatencyMs := tkSnapshotGatewayTransferLatencyMs(in.C)
	h.submitUsageRecordTask(in.C.Request.Context(), func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.RecordUsageInput{
			Result:             in.Result,
			QuotaPlatform:      quotaPlatform,
			APIKey:             in.APIKey,
			User:               in.APIKey.User,
			Account:            in.Account,
			Subscription:       in.Subscription,
			PricingAt:          in.PricingAt,
			InboundEndpoint:    inboundEndpoint,
			UpstreamEndpoint:   upstreamEndpoint,
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			SessionID:          sessionID,
			GatewayLatencyMs:   gatewayLatencyMs,
			ChannelUsageFields: in.ChannelUsageFields,
		}); err != nil {
			in.ReqLog.Error(in.LogFailedEvent,
				zap.Int64("account_id", in.Account.ID),
				zap.Error(err),
			)
		}
	})
}
