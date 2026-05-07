package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TK companion to upstream handleGeminiUpstreamError in gemini_messages_compat_service.go.
//
// 为什么独立成文件：
// upstream handleGeminiUpstreamError 对所有 429 一律走账号级限流（SetRateLimited）。
// 这对 API Key / AI Studio OAuth 是合理的（整个账号一起被限），但对 Code Assist
// OAuth 不准确：cloudcode-pa.googleapis.com 会针对**单个模型**返回
// MODEL_CAPACITY_EXHAUSTED（model 名出现在 ErrorInfo.metadata.model），同一账号
// 的其他模型仍然可用。把这种 per-model 限流写成账号级限流会：
//  1. 让整个账号在 Code Assist tier cooldown（约 11h）期间被打成「不可调度」，
//     其他模型完全不能用；
//  2. 前端 AccountStatusIndicator.vue 走账号级 isRateLimited 分支，只显示「限流中
//     10h 59m 自动恢复 429」，operator 看不到具体哪个模型限流。
//
// 与 antigravity 在 antigravity_gateway_service.go::handleUpstreamError 429 分支
// 已经做的 SetModelRateLimit 行为对齐——antigravity 走 resolveFinalAntigravityModelKey
// 把 requestedModel 转成最终 mapping 后的 key，这里直接用上游响应里的
// metadata.model，因为 platform=gemini 没有默认 mapping（GetMappedModel 在没有
// 用户 model_mapping 时返回 requestedModel 本身，与上游报错的 model 一致；
// 用户配置了 mapping 时，请求被映射后发出，上游报错里的 model 也是映射后的值，
// 仍然与下次调度查询用的 key 一致）。
//
// 历史背景：
// 2026-05-06 prod：claude-code 通过 /v1/messages → gemini-pa group → gemini-3.1-pro-preview
// 触发 429 RESOURCE_EXHAUSTED + MODEL_CAPACITY_EXHAUSTED；同账号 gemini-2.5-flash
// 仍然可用，但 UI 只能看到账号级「限流中 10h 59m」，无法判断具体哪个模型限流。
//
// 何时拆掉本文件：
// 如果 upstream 把 per-model rate limit 内化（不太可能：upstream 没有
// SetModelRateLimit 抽象），可以删掉本文件并将 call-site 还原。

// tryGeminiCodeAssistApplyModelRateLimit attempts to set a per-model rate limit
// when a Gemini Code Assist 429 carries a model-specific signal.
//
// Returns true iff:
//   - account is platform=gemini Code Assist OAuth (cloudcode-pa upstream), and
//   - body parses as a Google RPC error containing an ErrorInfo with a
//     non-empty metadata.model, or the caller provides a non-empty fallback
//     upstream model, and
//   - SetModelRateLimit succeeds.
//
// On true, the caller MUST skip its own account-level rate-limit fallback.
// On false (including non-Code-Assist accounts or no model key), the caller
// continues with the existing account-level path.
func (s *GeminiMessagesCompatService) tryGeminiCodeAssistApplyModelRateLimit(
	ctx context.Context, account *Account, body []byte, fallbackModel string,
) bool {
	if account == nil || !account.IsGeminiCodeAssist() {
		return false
	}

	modelName := extractGeminiCodeAssistRateLimitedModel(body)
	if modelName == "" {
		fallbackModel = strings.TrimSpace(fallbackModel)
		modelScoped := isGeminiCodeAssistModelScopedRateLimit(body)
		if fallbackModel == "" || !modelScoped {
			logger.LegacyPrintf("service.gemini_messages_compat",
				"[Gemini 429] tk_model_rate_limit_no_model account=%d fallback_model=%s model_scoped=%v (falling back to account-level)",
				account.ID, fallbackModel, modelScoped)
			return false
		}
		modelName = fallbackModel
	}

	// Reset time: prefer the upstream signal (quotaResetDelay / retryDelay),
	// fall back to the Code Assist tier cooldown — same source the
	// account-level path uses, so per-model behavior is just narrower scope,
	// not a different policy.
	var resetTime time.Time
	if ts := ParseGeminiRateLimitResetTime(body); ts != nil {
		resetTime = time.Unix(*ts, 0)
	} else {
		cooldown := geminiCooldownForTier(account.GeminiTierID())
		if s.rateLimitService != nil {
			cooldown = s.rateLimitService.GeminiCooldown(ctx, account)
		}
		resetTime = time.Now().Add(cooldown)
	}

	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, modelName, resetTime); err != nil {
		logger.LegacyPrintf("service.gemini_messages_compat",
			"[Gemini 429] tk_model_rate_limit_set_failed account=%d model=%s err=%v (falling back to account-level)",
			account.ID, modelName, err)
		return false
	}

	logger.LegacyPrintf("service.gemini_messages_compat",
		"[Gemini 429] tk_model_rate_limited account=%d (Code Assist) model=%s reset_in=%v",
		account.ID, modelName, time.Until(resetTime).Truncate(time.Second))
	return true
}

// extractGeminiCodeAssistRateLimitedModel returns the rate-limited model name
// found in a Google RPC error body's ErrorInfo.metadata.model, or "" if no
// per-model signal is present.
//
// We accept any reason (MODEL_CAPACITY_EXHAUSTED, RATE_LIMIT_EXCEEDED with
// model metadata, etc.) — the discriminator between per-model and account-wide
// is the **presence** of metadata.model. Account-wide quota errors (daily
// quota, account suspended, …) do not carry a model name.
func extractGeminiCodeAssistRateLimitedModel(body []byte) string {
	details := geminiErrorDetails(body)
	if len(details) == 0 {
		return ""
	}
	for _, dm := range details {
		atType, _ := dm["@type"].(string)
		if atType != googleRPCTypeErrorInfo {
			continue
		}
		meta, ok := dm["metadata"].(map[string]any)
		if !ok {
			continue
		}
		model, _ := meta["model"].(string)
		model = strings.TrimSpace(model)
		if model != "" {
			return model
		}
	}
	return ""
}

func isGeminiCodeAssistModelScopedRateLimit(body []byte) bool {
	details := geminiErrorDetails(body)
	if len(details) == 0 {
		return false
	}
	for _, dm := range details {
		atType, _ := dm["@type"].(string)
		if atType != googleRPCTypeErrorInfo {
			continue
		}
		reason, _ := dm["reason"].(string)
		if strings.TrimSpace(reason) == googleRPCReasonModelCapacityExhausted {
			return true
		}
	}
	return false
}

func geminiErrorDetails(body []byte) []map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		return nil
	}
	details, ok := errObj["details"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(details))
	for _, d := range details {
		dm, ok := d.(map[string]any)
		if ok {
			out = append(out, dm)
		}
	}
	return out
}
