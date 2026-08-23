package handler

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) tkHTTPRequestPricingContext(ctx context.Context, groupID *int64) (context.Context, time.Time) {
	return h.gatewayService.WithOpenAIRequestPricingContext(ctx, groupID)
}

type tkHTTPForwardUsageSubmitInput struct {
	C                  *gin.Context
	APIKey             *service.APIKey
	Account            *service.Account
	Subscription       *service.UserSubscription
	Subject            middleware2.AuthSubject
	Hold               *tkHoldHandle
	Body               []byte
	ReqModel           string
	PricingAt          time.Time
	ChannelMapping     service.ChannelMappingResult
	LogComponent       string
	LogFailedEventName string
}

func (h *OpenAIGatewayHandler) tkSubmitHTTPForwardUsage(res *service.OpenAIForwardResult, in tkHTTPForwardUsageSubmitInput) {
	if res == nil {
		return
	}
	userAgent := in.C.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(in.C)
	requestPayloadHash := service.HashUsageRequestPayload(in.Body)
	quotaPlatform := service.QuotaPlatform(in.C.Request.Context(), in.APIKey)
	sessionID := service.ExtractClientSessionID(in.C)
	tkHoldRequestID := in.Hold.HandOffToSettlement()
	cyberBlocked := service.GetOpsCyberPolicy(in.C) != nil
	gatewayLatencyMs := tkSnapshotGatewayTransferLatencyMs(in.C)
	h.submitOpenAIUsageRecordTask(in.C.Request.Context(), res, func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result:             res,
			APIKey:             in.APIKey,
			User:               in.APIKey.User,
			Account:            in.Account,
			Subscription:       in.Subscription,
			InboundEndpoint:    GetInboundEndpoint(in.C),
			UpstreamEndpoint:   resolveOpenAIUpstreamEndpoint(in.C, in.Account, res),
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			RequestPayloadHash: requestPayloadHash,
			APIKeyService:      h.apiKeyService,
			TkHoldRequestID:    tkHoldRequestID,
			QuotaPlatform:      quotaPlatform,
			SessionID:          sessionID,
			GatewayLatencyMs:   gatewayLatencyMs,
			ChannelUsageFields: clientRequestedUsageFields(in.C, in.ChannelMapping, in.ReqModel, res.UpstreamModel),
			PricingAt:          in.PricingAt,
			CyberBlocked:       cyberBlocked,
		}); err != nil {
			logger.L().With(
				zap.String("component", in.LogComponent),
				zap.Int64("user_id", in.Subject.UserID),
				zap.Int64("api_key_id", in.APIKey.ID),
				zap.Any("group_id", in.APIKey.GroupID),
				zap.String("model", in.ReqModel),
				zap.Int64("account_id", in.Account.ID),
			).Error(in.LogFailedEventName, zap.Error(err))
		}
	})
}

type tkOpenAISimpleUsageSubmitInput struct {
	C                  *gin.Context
	APIKey             *service.APIKey
	Account            *service.Account
	Subscription       *service.UserSubscription
	Subject            middleware2.AuthSubject
	Hold               *tkHoldHandle
	Result             *service.OpenAIForwardResult
	ReqModel           string
	ChannelMapping     service.ChannelMappingResult
	LogComponent       string
	LogFailedEventName string
}

func (h *OpenAIGatewayHandler) tkSubmitOpenAISimpleForwardUsage(in tkOpenAISimpleUsageSubmitInput) {
	userAgent := in.C.GetHeader("User-Agent")
	clientIP := ip.GetClientIP(in.C)
	quotaPlatform := service.QuotaPlatform(in.C.Request.Context(), in.APIKey)
	tkHoldRequestID := in.Hold.HandOffToSettlement()
	gatewayLatencyMs := tkSnapshotGatewayTransferLatencyMs(in.C)
	upstreamModelForUsage := ""
	if in.Result != nil {
		upstreamModelForUsage = in.Result.UpstreamModel
	}
	h.submitOpenAIUsageRecordTask(in.C.Request.Context(), in.Result, func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
			Result:             in.Result,
			APIKey:             in.APIKey,
			User:               in.APIKey.User,
			Account:            in.Account,
			Subscription:       in.Subscription,
			InboundEndpoint:    GetInboundEndpoint(in.C),
			UpstreamEndpoint:   GetUpstreamEndpoint(in.C, in.Account.Platform),
			UserAgent:          userAgent,
			IPAddress:          clientIP,
			APIKeyService:      h.apiKeyService,
			TkHoldRequestID:    tkHoldRequestID,
			QuotaPlatform:      quotaPlatform,
			GatewayLatencyMs:   gatewayLatencyMs,
			ChannelUsageFields: in.ChannelMapping.ToUsageFields(in.ReqModel, upstreamModelForUsage),
		}); err != nil {
			logger.L().With(
				zap.String("component", in.LogComponent),
				zap.Int64("user_id", in.Subject.UserID),
				zap.Int64("api_key_id", in.APIKey.ID),
				zap.Any("group_id", in.APIKey.GroupID),
				zap.String("model", in.ReqModel),
				zap.Int64("account_id", in.Account.ID),
			).Error(in.LogFailedEventName, zap.Error(err))
		}
	})
}
