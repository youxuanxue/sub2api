package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

const upstreamModelNotFoundCooldown = 30 * time.Minute
const upstreamModelNotFoundReason = "upstream_404_model_not_found"
const upstreamCodexPlanGatedModelCooldown = 30 * time.Minute
const upstreamCodexPlanGatedModelReason = "upstream_400_codex_plan_gated_model"

// HandleUpstreamModelNotFound marks the requested model as temporarily
// unavailable on the account when the upstream deterministically reports it
// cannot serve that model: a 404 model-not-found, or the Codex 400 rejecting a
// plan-gated model on a ChatGPT OAuth account. Returning true tells the caller
// to fail the current attempt over to another account; the scheduler skips the
// (account, model) pair via IsSchedulableForModelWithContext until the
// cooldown expires, instead of re-selecting an account that can never serve
// the model.
func (s *RateLimitService) HandleUpstreamModelNotFound(ctx context.Context, account *Account, requestedModel string, statusCode int, responseBody []byte) bool {
	if s == nil || account == nil || s.accountRepo == nil {
		return false
	}
	if !account.ShouldHandleErrorCode(statusCode) {
		return false
	}
	var cooldown time.Duration
	var reason string
	switch {
	case isUpstreamModelNotFoundError(statusCode, responseBody):
		cooldown, reason = upstreamModelNotFoundCooldown, upstreamModelNotFoundReason
	case isOpenAIOAuthAccount(account) && isOpenAICodexPlanGatedModelError(statusCode, responseBody):
		cooldown, reason = upstreamCodexPlanGatedModelCooldown, upstreamCodexPlanGatedModelReason
	default:
		return false
	}
	if account.Platform == PlatformAnthropic {
		slog.Info("anthropic_model_not_found_skip_penalty",
			"account_id", account.ID,
			"requested_model", strings.TrimSpace(requestedModel))
		return false
	}
	modelKey := modelRateLimitKeyForUpstreamModelNotFound(ctx, account, requestedModel)
	if modelKey == "" {
		return false
	}
	if reason == upstreamModelNotFoundReason && account.Platform == PlatformOpenAI && account.IsOAuth() && account.IsModelSupported(requestedModel) {
		if !IsGPTImageGenerationModel(requestedModel) && !IsGPTImageGenerationModel(modelKey) {
			return false
		}
	}
	if shouldSkipCodexPlanGatedImageModelCooldown(ctx, reason, requestedModel, modelKey) {
		return true
	}
	resetAt := time.Now().Add(cooldown)
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, modelKey, resetAt, reason); err != nil {
		slog.Warn("upstream_model_not_found_set_model_rate_limit_failed", "account_id", account.ID, "model", modelKey, "reason", reason, "error", err)
		return true
	}
	slog.Info("upstream_model_not_found_model_rate_limited", "account_id", account.ID, "model", modelKey, "reason", reason, "reset_at", resetAt)
	return true
}

// shouldSkipCodexPlanGatedImageModelCooldown 判断这次 Codex plan-gated 400 是否
// 属于"图片模型被文本端点拒绝"。
//
// 这类错误是确定性的端点错配，不是账号能力缺失：同一账号通过 /v1/images/* 依然
// 能出图。在这里写 per-model 冷却，会让一次用错端点的请求把整个号池对正确的生图
// 端点下线（#4828）。
//
// 但请求本身就从 /v1/images/* 入站时不适用——那种情况下被拒说明账号确实不具备
// 该模型能力，冷却是必要的刹车：没有它，每个生图请求都会完整走一遍号池，对上游
// 形成无上界的 400 放大。
//
// 请求模型与最终冷却键都要判：冷却键走的是 account.GetMappedModel，账号可能把
// 文本别名映射到 gpt-image-*，只判请求模型会漏掉这种形态。
func shouldSkipCodexPlanGatedImageModelCooldown(ctx context.Context, reason, requestedModel, modelKey string) bool {
	if reason != upstreamCodexPlanGatedModelReason {
		return false
	}
	if OpenAIImagesEndpointFromContext(ctx) {
		return false
	}
	return IsGPTImageGenerationModel(requestedModel) || IsGPTImageGenerationModel(modelKey)
}

func modelRateLimitKeyForUpstreamModelNotFound(ctx context.Context, account *Account, requestedModel string) string {
	modelKey := strings.TrimSpace(requestedModel)
	if account == nil || modelKey == "" {
		return modelKey
	}
	if account.Platform == PlatformAntigravity {
		if resolved := strings.TrimSpace(resolveFinalAntigravityModelKey(ctx, account, modelKey)); resolved != "" {
			return resolved
		}
		return modelKey
	}
	if mapped := strings.TrimSpace(account.GetMappedModel(modelKey)); mapped != "" {
		return mapped
	}
	return modelKey
}
