package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// TK: pre-flight balance HOLD wiring for the OpenAI-compatible gateway (chat /
// responses / messages / embeddings / images / video — the balance-billed
// overdraft surface; Anthropic-native traffic is predominantly subscription,
// which does not touch balance). Estimates a request reserve, deducts it
// atomically before forward, and releases at request end. Explicit output
// ceilings can be reserved as upper bounds; omitted token ceilings use the
// handler's low default reserve to protect UX. See usage_billing_hold_tk.go
// for the concurrency argument and billing_service_tk_hold.go for the pricing
// formulas.

// tkReserveBalanceHold reserves a hold via the repository's narrow hold
// capability. Returns:
//   - held=true            → balance reserved; the caller owns the release
//     (request-end refund, or hand-off to settlement).
//   - held=false, reject=true  → insufficient balance; caller rejects with 403.
//   - held=false, reject=false → not gated (no hold capability, or amount ≤ 0 —
//     e.g. an unpriced chat model that bills $0 by design); let post-hoc billing
//     proceed unchanged.
func tkReserveBalanceHold(ctx context.Context, repo UsageBillingRepository, requestID string, userID, apiKeyID int64, amount float64) (held bool, reject bool, err error) {
	applier, ok := repo.(UsageBillingHoldApplier)
	if !ok {
		return false, false, nil
	}
	if amount <= 0 || requestID == "" {
		return false, false, nil
	}
	reserved, err := applier.ReserveBalanceHold(ctx, &HoldCommand{
		RequestID: requestID,
		UserID:    userID,
		APIKeyID:  apiKeyID,
		Amount:    amount,
	})
	if err != nil {
		return false, false, err
	}
	if !reserved {
		return false, true, nil
	}
	return true, false, nil
}

// tkReleaseBalanceHold refunds a reservation; idempotent and best-effort (the
// hold reconciler is the backstop for any release that does not run).
func tkReleaseBalanceHold(ctx context.Context, repo UsageBillingRepository, requestID string) {
	applier, ok := repo.(UsageBillingHoldApplier)
	if !ok || requestID == "" {
		return
	}
	_, _ = applier.ReleaseBalanceHold(ctx, requestID)
}

// tkHoldRateMultiplier resolves the SAME user×group rate multiplier the billing
// path applies to the balance, so the hold is not reduced by a smaller
// multiplier. Mirrors the resolution in RecordUsage (openai_gateway_service.go).
func (s *OpenAIGatewayService) tkHoldRateMultiplier(ctx context.Context, user *User, apiKey *APIKey) float64 {
	multiplier := 1.0
	if s.cfg != nil && s.cfg.Default.RateMultiplier > 0 {
		multiplier = s.cfg.Default.RateMultiplier
	}
	if user != nil && apiKey != nil && apiKey.GroupID != nil && s.userGroupRateResolver != nil {
		groupDefault := 1.0
		if apiKey.Group != nil {
			groupDefault = apiKey.Group.RateMultiplier
		}
		multiplier = s.userGroupRateResolver.Resolve(ctx, user.ID, *apiKey.GroupID, groupDefault)
	}
	return multiplier
}

// ResolveUserGroupRateMultiplier resolves the same cached multiplier used by OpenAI usage billing.
func (s *OpenAIGatewayService) ResolveUserGroupRateMultiplier(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	if s == nil {
		return groupDefaultMultiplier
	}
	resolver := s.userGroupRateResolver
	if resolver == nil {
		resolver = newUserGroupRateResolver(nil, nil, resolveUserGroupRateCacheTTL(s.cfg), nil, "service.openai_gateway")
	}
	return resolver.Resolve(ctx, userID, groupID, groupDefaultMultiplier)
}

// tkHoldGatingDisabled reports whether hold gating is off for this deployment:
// simple mode records usage but never bills, so a reservation would only pin
// user money until the reconciler refunds it.
func (s *OpenAIGatewayService) tkHoldGatingDisabled() bool {
	return s.cfg != nil && s.cfg.RunMode == config.RunModeSimple
}

// TkReserveTokenHold estimates a token-cost reserve and deducts it.
// requestID must be derived from the usage-billing request id
// (TkResolveUsageBillingRequestID) so reconciliation can anchor a hold to its
// request. promptTokens must be an UPPER BOUND on the request's input tokens
// (callers over-estimate). maxOutputTokens is an upper bound only when callers
// pass an explicit client ceiling; omitted output ceilings may use a low
// default reserve. Embeddings pass maxOutputTokens=0.
//
// Returns held (caller owns the release) and reject (caller returns 403). A
// pricing/DB failure is fail-open (held=false, reject=false) + an ALERT log: a
// reservation outage must not deny service (availability wins, same regime as
// unpriced-chat serve-and-alert), it only narrows the guarantee until resolved.
func (s *OpenAIGatewayService) TkReserveTokenHold(ctx context.Context, requestID, model, serviceTier string, user *User, apiKey *APIKey, promptTokens, maxOutputTokens int) (held bool, reject bool) {
	if s == nil || user == nil || apiKey == nil || requestID == "" || s.tkHoldGatingDisabled() {
		return false, false
	}
	multiplier := s.tkHoldRateMultiplier(ctx, user, apiKey)
	amount, err := s.billingService.EstimateTokenHold(model, serviceTier, promptTokens, maxOutputTokens, multiplier)
	if err != nil {
		// Unpriced model: chat bills $0 by design and serves — do not gate.
		return false, false
	}
	held, reject, err = tkReserveBalanceHold(ctx, s.usageBillingRepo, requestID, user.ID, apiKey.ID, amount)
	if err != nil {
		logger.L().Error("openai_gateway.hold_reserve_failed",
			zap.String("request_id", requestID),
			zap.Int64("user_id", user.ID),
			zap.Float64("amount", amount),
			zap.Error(err),
		)
		return false, false
	}
	return held, reject
}

// TkReserveImageHold estimates an upper-bound image-generation cost and
// reserves it. n is the REQUESTED image count (actual delivers ≤ n). Same
// fail-open posture as TkReserveTokenHold.
//
// Upper-bound construction: billing resolves the size tier from the upstream
// OUTPUT size (which may exceed the requested size), so the estimate takes the
// MAX over all billing tiers (1K/2K/4K) of the same cost calculation billing
// will run — the channel per-request/image price when channel-priced, the
// group/litellm image price otherwise. Known gap, deliberately accepted: a
// channel priced in TOKEN mode bills an image request by its (unboundable)
// image-output tokens; the tier-max image estimate still collapses the
// concurrent-overdraft amplification there, but is not a proven bound.
func (s *OpenAIGatewayService) TkReserveImageHold(ctx context.Context, requestID, model string, user *User, apiKey *APIKey, n int) (held bool, reject bool) {
	if s == nil || user == nil || apiKey == nil || requestID == "" || s.tkHoldGatingDisabled() {
		return false, false
	}
	multiplier := resolveImageRateMultiplier(apiKey, s.tkHoldRateMultiplier(ctx, user, apiKey))
	amount := s.tkEstimateImageHoldAmount(ctx, model, apiKey, n, multiplier)
	held, reject, err := tkReserveBalanceHold(ctx, s.usageBillingRepo, requestID, user.ID, apiKey.ID, amount)
	if err != nil {
		logger.L().Error("openai_gateway.hold_reserve_failed",
			zap.String("request_id", requestID),
			zap.Int64("user_id", user.ID),
			zap.Float64("amount", amount),
			zap.Error(err),
		)
		return false, false
	}
	return held, reject
}

// tkEstimateImageHoldAmount mirrors calculateOpenAIImageCost with at-submit
// upper-bound inputs: requested count, MAX over billing tiers.
func (s *OpenAIGatewayService) tkEstimateImageHoldAmount(ctx context.Context, model string, apiKey *APIKey, n int, multiplier float64) float64 {
	if n <= 0 {
		n = 1
	}
	var groupConfig *ImagePriceConfig
	if apiKey != nil && apiKey.Group != nil {
		groupConfig = &ImagePriceConfig{
			Price1K: apiKey.Group.ImagePrice1K,
			Price2K: apiKey.Group.ImagePrice2K,
			Price4K: apiKey.Group.ImagePrice4K,
		}
	}
	resolved := s.resolveOpenAIChannelPricing(ctx, model, apiKey)
	amount := 0.0
	for _, tier := range []string{ImageBillingSize1K, ImageBillingSize2K, ImageBillingSize4K} {
		if resolved != nil && (resolved.Mode == BillingModePerRequest || resolved.Mode == BillingModeImage) && apiKey.Group != nil {
			gid := apiKey.Group.ID
			cost, err := s.billingService.CalculateCostUnified(CostInput{
				Ctx:            ctx,
				Model:          model,
				GroupID:        &gid,
				RequestCount:   n,
				SizeTier:       tier,
				RateMultiplier: multiplier,
				Resolver:       s.resolver,
				Resolved:       resolved,
			})
			if err == nil && cost != nil {
				amount = maxFloat(amount, cost.ActualCost)
				continue
			}
		}
		amount = maxFloat(amount, s.billingService.EstimateImageHold(model, tier, n, groupConfig, multiplier))
	}
	return amount
}

// TkReserveVideoHold reserves the exact cost the video submit path will bill:
// CalculateVideoCost over the same request-derived seconds, same multiplier.
// Same fail-open posture as TkReserveTokenHold.
func (s *OpenAIGatewayService) TkReserveVideoHold(ctx context.Context, requestID, model string, user *User, apiKey *APIKey, seconds int64) (held bool, reject bool) {
	if s == nil || user == nil || apiKey == nil || requestID == "" || s.tkHoldGatingDisabled() {
		return false, false
	}
	multiplier := s.tkHoldRateMultiplier(ctx, user, apiKey)
	amount := s.billingService.EstimateVideoHold(model, seconds, multiplier)
	held, reject, err := tkReserveBalanceHold(ctx, s.usageBillingRepo, requestID, user.ID, apiKey.ID, amount)
	if err != nil {
		logger.L().Error("openai_gateway.hold_reserve_failed",
			zap.String("request_id", requestID),
			zap.Int64("user_id", user.ID),
			zap.Float64("amount", amount),
			zap.Error(err),
		)
		return false, false
	}
	return held, reject
}

// TkReleaseHold refunds a reservation at request end. Detaches from the
// request context (which may already be cancelled) so the refund still runs.
func (s *OpenAIGatewayService) TkReleaseHold(ctx context.Context, requestID string) {
	if s == nil || requestID == "" {
		return
	}
	relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tkReleaseBalanceHold(relCtx, s.usageBillingRepo, requestID)
}
