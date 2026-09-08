package handler

import (
	"context"
	"errors"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// tkHandleMessagesPromptTooLongFallback handles Antigravity PromptTooLongError
// by optionally switching to FallbackGroupIDOnInvalidRequest for a single
// retry. Returns:
//
//   - retry=true, newAPIKey set: caller should break the inner failover loop
//     and retry with the fallback-bound key (subscription cleared, fallbackUsed=true).
//   - handled=true, retry=false: response already written; caller must return.
//   - handled=false: err was not PromptTooLongError; continue normal error handling.
//
// cloneSource is the original request API key (Messages outer apiKey); currentAPIKey
// is the key bound for this attempt (may already be a prior fallback).
func (h *GatewayHandler) tkHandleMessagesPromptTooLongFallback(
	c *gin.Context,
	reqLog *zap.Logger,
	err error,
	account *service.Account,
	cloneSource *service.APIKey,
	currentAPIKey *service.APIKey,
	fallbackGroupID *int64,
	fallbackUsed bool,
	streamStarted bool,
) (newAPIKey *service.APIKey, retry bool, handled bool) {
	if service.CandidateRequestFromContext(c.Request.Context()) != nil {
		fallbackUsed = true
	}
	var promptTooLongErr *service.PromptTooLongError
	if !errors.As(err, &promptTooLongErr) {
		return nil, false, false
	}
	reqLog.Warn("gateway.prompt_too_long_from_antigravity",
		zap.Any("current_group_id", currentAPIKey.GroupID),
		zap.Any("fallback_group_id", fallbackGroupID),
		zap.Bool("fallback_used", fallbackUsed),
	)
	if !fallbackUsed && fallbackGroupID != nil && *fallbackGroupID > 0 {
		fallbackGroup, resolveErr := h.gatewayService.ResolveGroupByID(c.Request.Context(), *fallbackGroupID)
		if resolveErr != nil {
			reqLog.Warn("gateway.resolve_fallback_group_failed", zap.Int64("fallback_group_id", *fallbackGroupID), zap.Error(resolveErr))
			_ = h.antigravityGatewayService.WriteMappedClaudeError(c, account, promptTooLongErr.StatusCode, promptTooLongErr.RequestID, promptTooLongErr.Body)
			return nil, false, true
		}
		if fallbackGroup.Platform != service.PlatformAnthropic ||
			fallbackGroup.SubscriptionType == service.SubscriptionTypeSubscription ||
			fallbackGroup.FallbackGroupIDOnInvalidRequest != nil {
			reqLog.Warn("gateway.fallback_group_invalid",
				zap.Int64("fallback_group_id", fallbackGroup.ID),
				zap.String("fallback_platform", fallbackGroup.Platform),
				zap.String("fallback_subscription_type", fallbackGroup.SubscriptionType),
			)
			_ = h.antigravityGatewayService.WriteMappedClaudeError(c, account, promptTooLongErr.StatusCode, promptTooLongErr.RequestID, promptTooLongErr.Body)
			return nil, false, true
		}
		fallbackAPIKey := cloneAPIKeyWithGroup(cloneSource, fallbackGroup)
		if billingErr := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), fallbackAPIKey.User, fallbackAPIKey, fallbackGroup, nil, service.PlatformFromAPIKey(fallbackAPIKey)); billingErr != nil {
			status, code, message, retryAfter := billingErrorDetails(billingErr)
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.Itoa(retryAfter))
			}
			h.handleStreamingAwareError(c, status, code, message, streamStarted)
			return nil, false, true
		}
		// 兜底重试按"直接请求兜底分组"处理：清除强制平台，允许按分组平台调度
		ctx := context.WithValue(c.Request.Context(), ctxkey.ForcePlatform, "")
		c.Request = c.Request.WithContext(ctx)
		return fallbackAPIKey, true, true
	}
	_ = h.antigravityGatewayService.WriteMappedClaudeError(c, account, promptTooLongErr.StatusCode, promptTooLongErr.RequestID, promptTooLongErr.Body)
	return nil, false, true
}
