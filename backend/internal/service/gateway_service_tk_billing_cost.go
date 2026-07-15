package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TK billing cost calculation: channel pricing resolution, token/image cost
// calculation, and usage-log construction for GatewayService.RecordUsage.
// Extracted from gateway_service.go to reduce upstream merge surface.

// getUserGroupRateMultiplier resolves the effective rate multiplier for a
// user x group pair, with fallback to groupDefaultMultiplier.
func (s *GatewayService) getUserGroupRateMultiplier(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	if s == nil {
		return groupDefaultMultiplier
	}
	resolver := s.userGroupRateResolver
	if resolver == nil {
		resolver = newUserGroupRateResolver(
			s.userGroupRateRepo,
			s.userGroupRateCache,
			resolveUserGroupRateCacheTTL(s.cfg),
			&s.userGroupRateSF,
			"service.gateway",
		)
	}
	return resolver.Resolve(ctx, userID, groupID, groupDefaultMultiplier)
}

// calculateRecordUsageCost 根据请求类型和选项计算费用。
func (s *GatewayService) calculateRecordUsageCost(
	ctx context.Context,
	result *ForwardResult,
	apiKey *APIKey,
	billingModel string,
	multiplier float64,
	imageMultiplier float64,
	opts *recordUsageOpts,
) *CostBreakdown {
	// 图片生成：渠道定价为 token 计费时走 token 路径，否则走图片计费
	if result.ImageCount > 0 {
		if resolved := s.resolveChannelPricing(ctx, billingModel, apiKey); resolved != nil && resolved.Mode == BillingModeToken {
			return s.calculateTokenCost(ctx, result, apiKey, billingModel, multiplier, opts)
		}
		return s.calculateImageCost(ctx, result, apiKey, billingModel, imageMultiplier)
	}

	// Token 计费
	return s.calculateTokenCost(ctx, result, apiKey, billingModel, multiplier, opts)
}

// resolveChannelPricing 检查指定模型是否存在渠道级别定价。
// 返回非 nil 的 ResolvedPricing 表示有渠道定价，nil 表示走默认定价路径。
func (s *GatewayService) resolveChannelPricing(ctx context.Context, billingModel string, apiKey *APIKey) *ResolvedPricing {
	if s.resolver == nil || apiKey.Group == nil {
		return nil
	}
	gid := apiKey.Group.ID
	resolved := s.resolver.Resolve(ctx, PricingInput{Model: billingModel, GroupID: &gid})
	if resolved.Source == PricingSourceChannel {
		return resolved
	}
	return nil
}

// calculateImageCost 计算图片生成费用：渠道级别定价优先，否则走按次计费。
func (s *GatewayService) calculateImageCost(
	ctx context.Context,
	result *ForwardResult,
	apiKey *APIKey,
	billingModel string,
	multiplier float64,
) *CostBreakdown {
	sizeTier := NormalizeImageBillingTierOrDefault(result.ImageSize)
	if apiKeyHasConfiguredImagePrice(apiKey, sizeTier) {
		groupConfig := &ImagePriceConfig{
			Price1K: apiKey.Group.ImagePrice1K,
			Price2K: apiKey.Group.ImagePrice2K,
			Price4K: apiKey.Group.ImagePrice4K,
		}
		return s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, groupConfig, multiplier)
	}
	if resolved := s.resolveChannelPricing(ctx, billingModel, apiKey); resolved != nil {
		tokens := UsageTokens{
			InputTokens:       result.Usage.InputTokens,
			OutputTokens:      result.Usage.OutputTokens,
			ImageOutputTokens: result.Usage.ImageOutputTokens,
		}
		gid := apiKey.Group.ID
		cost, err := s.billingService.CalculateCostUnified(CostInput{
			Ctx:            ctx,
			Model:          billingModel,
			GroupID:        &gid,
			Tokens:         tokens,
			RequestCount:   result.ImageCount,
			SizeTier:       sizeTier,
			RateMultiplier: multiplier,
			Resolver:       s.resolver,
			Resolved:       resolved,
		})
		if err != nil {
			logger.LegacyPrintf("service.gateway", "Calculate image token cost failed: %v", err)
			return &CostBreakdown{ActualCost: 0}
		}
		return cost
	}

	var groupConfig *ImagePriceConfig
	if apiKey.Group != nil {
		groupConfig = &ImagePriceConfig{
			Price1K: apiKey.Group.ImagePrice1K,
			Price2K: apiKey.Group.ImagePrice2K,
			Price4K: apiKey.Group.ImagePrice4K,
		}
	}
	return s.billingService.CalculateImageCost(billingModel, sizeTier, result.ImageCount, groupConfig, multiplier)
}

// calculateTokenCost 计算 Token 计费：根据 opts 决定走普通/长上下文/渠道统一计费。
func (s *GatewayService) calculateTokenCost(
	ctx context.Context,
	result *ForwardResult,
	apiKey *APIKey,
	billingModel string,
	multiplier float64,
	opts *recordUsageOpts,
) *CostBreakdown {
	tokens := UsageTokens{
		InputTokens:           result.Usage.InputTokens,
		OutputTokens:          result.Usage.OutputTokens,
		CacheCreationTokens:   result.Usage.CacheCreationInputTokens,
		CacheReadTokens:       result.Usage.CacheReadInputTokens,
		CacheCreation5mTokens: result.Usage.CacheCreation5mTokens,
		CacheCreation1hTokens: result.Usage.CacheCreation1hTokens,
		ImageOutputTokens:     result.Usage.ImageOutputTokens,
	}

	var cost *CostBreakdown
	var err error

	// 优先尝试渠道定价 → CalculateCostUnified
	if resolved := s.resolveChannelPricing(ctx, billingModel, apiKey); resolved != nil {
		gid := apiKey.Group.ID
		cost, err = s.billingService.CalculateCostUnified(CostInput{
			Ctx:            ctx,
			Model:          billingModel,
			GroupID:        &gid,
			Tokens:         tokens,
			RequestCount:   1,
			RateMultiplier: multiplier,
			Resolver:       s.resolver,
			Resolved:       resolved,
		})
	} else if opts.LongContextThreshold > 0 {
		// 长上下文双倍计费（如 Gemini 200K 阈值）
		cost, err = s.billingService.CalculateCostWithLongContext(billingModel, tokens, multiplier, opts.LongContextThreshold, opts.LongContextMultiplier)
	} else {
		cost, err = s.billingService.CalculateCost(billingModel, tokens, multiplier)
	}
	if err != nil {
		// TK (upstream Wei-Shaw/sub2api#1833 / #1544): surface pricing-missing as a
		// structured, observable zero-cost log instead of a silent ActualCost:0
		// leak — at parity with the OpenAI record-usage path. The P0 Feishu alert
		// is no longer fired here: it is consolidated into the result-side
		// "served but zero cost" probe (tkNotifyServedZeroCost) at the record-usage
		// site, which catches this error-path zero-cost AND the silent $0 paths
		// that never error. This call keeps only the grep-able structured log.
		logTokenCostPricingMissing(billingModel, apiKey, result, err)
		if isUsagePricingUnavailableError(err) {
			return &CostBreakdown{BillingMode: string(BillingModeToken)}
		}
		// Non-pricing-missing errors deliberately retain upstream gateway's
		// "swallow and record zero" semantics (the OpenAI record-usage path
		// instead propagates such errors). We keep the legacy behavior here to
		// avoid failing usage recording on unexpected cost-calc errors; only the
		// pricing-missing case above is upgraded to an observable marked record.
		return &CostBreakdown{ActualCost: 0}
	}
	return cost
}

// buildRecordUsageLog 构建使用日志并设置计费模式。
func (s *GatewayService) buildRecordUsageLog(
	ctx context.Context,
	input *recordUsageCoreInput,
	result *ForwardResult,
	apiKey *APIKey,
	user *User,
	account *Account,
	subscription *UserSubscription,
	requestedModel string,
	multiplier float64,
	imageMultiplier float64,
	accountRateMultiplier float64,
	billingType int8,
	cacheTTLOverridden bool,
	cost *CostBreakdown,
	opts *recordUsageOpts,
) *UsageLog {
	durationMs := int(result.Duration.Milliseconds())
	requestID := resolveUsageBillingRequestID(ctx, result.RequestID)
	usageLog := &UsageLog{
		UserID:                user.ID,
		APIKeyID:              apiKey.ID,
		AccountID:             account.ID,
		RequestID:             requestID,
		Model:                 result.Model,
		RequestedModel:        requestedModel,
		UpstreamModel:         optionalNonEqualStringPtr(result.UpstreamModel, result.Model),
		ReasoningEffort:       result.ReasoningEffort,
		BillingTier:           optionalTrimmedStringPtr(result.BillingTier),
		InboundEndpoint:       optionalTrimmedStringPtr(input.InboundEndpoint),
		UpstreamEndpoint:      optionalTrimmedStringPtr(input.UpstreamEndpoint),
		InputTokens:           result.Usage.InputTokens,
		OutputTokens:          result.Usage.OutputTokens,
		CacheCreationTokens:   result.Usage.CacheCreationInputTokens,
		CacheReadTokens:       result.Usage.CacheReadInputTokens,
		CacheCreation5mTokens: result.Usage.CacheCreation5mTokens,
		CacheCreation1hTokens: result.Usage.CacheCreation1hTokens,
		ImageOutputTokens:     result.Usage.ImageOutputTokens,
		RateMultiplier:        multiplier,
		AccountRateMultiplier: &accountRateMultiplier,
		BillingType:           billingType,
		BillingMode:           resolveBillingMode(result, cost),
		Stream:                result.Stream,
		DurationMs:            &durationMs,
		FirstTokenMs:          result.FirstTokenMs,
		ImageCount:            result.ImageCount,
		ImageSize:             optionalTrimmedStringPtr(result.ImageSize),
		ImageInputSize:        optionalTrimmedStringPtr(result.ImageInputSize),
		ImageOutputSize:       optionalTrimmedStringPtr(result.ImageOutputSize),
		ImageSizeSource:       optionalTrimmedStringPtr(result.ImageSizeSource),
		ImageSizeBreakdown:    result.ImageSizeBreakdown,
		CacheTTLOverridden:    cacheTTLOverridden,
		ChannelID:             optionalInt64Ptr(input.ChannelID),
		ModelMappingChain:     optionalTrimmedStringPtr(input.ModelMappingChain),
		UserAgent:             optionalTrimmedStringPtr(input.UserAgent),
		IPAddress:             optionalTrimmedStringPtr(input.IPAddress),
		GroupID:               apiKey.GroupID,
		SubscriptionID:        optionalSubscriptionID(subscription),
		CreatedAt:             time.Now(),
	}
	if result.ImageCount > 0 && (cost == nil || cost.BillingMode != string(BillingModeToken)) {
		usageLog.RateMultiplier = imageMultiplier
	}
	if cost != nil {
		usageLog.InputCost = cost.InputCost
		usageLog.OutputCost = cost.OutputCost
		usageLog.ImageOutputCost = cost.ImageOutputCost
		usageLog.CacheCreationCost = cost.CacheCreationCost
		usageLog.CacheReadCost = cost.CacheReadCost
		usageLog.TotalCost = cost.TotalCost
		usageLog.ActualCost = cost.ActualCost
	}

	return usageLog
}

// resolveBillingMode 根据计费结果和请求类型确定计费模式。
func resolveBillingMode(result *ForwardResult, cost *CostBreakdown) *string {
	var mode string
	switch {
	case cost != nil && cost.BillingMode != "":
		mode = cost.BillingMode
	case result.ImageCount > 0:
		mode = string(BillingModeImage)
	default:
		mode = string(BillingModeToken)
	}
	return &mode
}

func optionalSubscriptionID(subscription *UserSubscription) *int64 {
	if subscription != nil {
		return &subscription.ID
	}
	return nil
}
