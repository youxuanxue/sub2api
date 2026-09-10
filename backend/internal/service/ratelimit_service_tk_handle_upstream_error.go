package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

// tkDispatchHandleUpstreamError owns the post-prelude HandleUpstreamError path:
// pool-mode / custom-error carve-outs, model-not-found, Anthropic window limits,
// temp-unschedulable, and the status-code switch (including handle529).
func (s *RateLimitService) tkDispatchHandleUpstreamError(
	ctx context.Context,
	account *Account,
	statusCode int,
	headers http.Header,
	responseBody []byte,
	requestedModel []string,
	customErrorCodesEnabled bool,
) bool {
	var shouldDisable bool

	// Recompute for pool-mode / custom-error carve-outs (predicate is cheap;
	// prelude already applied the CloudWise cooldown early returns above).
	cloudwiseModelBalance402 := tkIsCloudwiseModelBalance402Response(account, statusCode, responseBody)

	// 池模式默认不标记本地账号状态；但管理员显式配置的临时不可调度规则优先。
	// 401 保留现有认证错误语义，不在这里改变池模式的认证处理。
	if account.IsPoolMode() && !customErrorCodesEnabled && account.Platform != PlatformAnthropic && !cloudwiseModelBalance402 {
		if statusCode != http.StatusUnauthorized && s.tryTempUnschedulable(ctx, account, statusCode, responseBody) {
			return true
		}
		slog.Info("pool_mode_error_skipped", "account_id", account.ID, "status_code", statusCode)
		return false
	}

	// apikey 类型账号：检查自定义错误码配置
	// 如果启用且错误码不在列表中，则不处理（不停止调度、不标记限流/过载）
	planGatedModel := isOpenAIOAuthAccount(account) && isOpenAICodexPlanGatedModelError(statusCode, responseBody)
	if !cloudwiseModelBalance402 && !planGatedModel && !account.ShouldHandleErrorCode(statusCode) && account.Platform != PlatformAnthropic {
		slog.Info("account_error_code_skipped", "account_id", account.ID, "status_code", statusCode)
		return false
	}

	if len(requestedModel) > 0 && s.HandleUpstreamModelNotFound(ctx, account, requestedModel[0], statusCode, responseBody) {
		return true
	}

	// Anthropic official 5h / 7d window exhaustion is a hard account limit.
	// It must take precedence over user-configured 429 temp-unsched rules,
	// otherwise a broad "rate limit" keyword rule can shorten a multi-hour
	// cooldown to a local temporary pause.
	if statusCode == http.StatusTooManyRequests && account.Platform == PlatformAnthropic {
		// 7d_oi 是 Fable 模型专属的 7d 窗口：只标记模型级限流，账号对其他模型仍可调度。
		fableLimited := s.persistAnthropicFableWindowLimit(ctx, account, headers)
		fableCreditsRequired := s.persistAnthropicFableCreditsRequired(ctx, account, headers, responseBody, firstRequestedModel(requestedModel))
		if s.persistAnthropicExhaustedWindowLimit(ctx, account, headers) {
			return false
		}
		if fableLimited || fableCreditsRequired {
			return false
		}
	}

	// 先尝试临时不可调度规则（401除外）
	// 如果匹配成功，直接返回，不执行后续禁用逻辑
	// 529 overload 走 handle529 全局冷却，优先于账号级 temp-unsched 规则。
	if statusCode != 401 && statusCode != 529 {
		if s.tryTempUnschedulable(ctx, account, statusCode, responseBody, firstRequestedModel(requestedModel)) {
			return true
		}
	}

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	if upstreamMsg != "" {
		upstreamMsg = truncateForLog([]byte(upstreamMsg), 512)
	}

	if account.Platform == PlatformAnthropic && tkIsAnthropicUsagePolicyBlock(upstreamMsg, responseBody) {
		return s.handleAnthropicUsagePolicyBlock(ctx, account, statusCode, upstreamMsg, responseBody)
	}

	if handled, quotaDisable := s.tkMaybeHandleKiroEndpointQuotaExhausted(ctx, account, upstreamMsg, responseBody); handled {
		return quotaDisable
	}

	switch statusCode {
	case 400:
		// "organization has been disabled" → 永久禁用
		if strings.Contains(strings.ToLower(upstreamMsg), "organization has been disabled") {
			msg := "Organization disabled (400): " + upstreamMsg
			s.handleAuthError(ctx, account, msg)
			shouldDisable = true
		} else if account.Platform == PlatformAnthropic && strings.Contains(strings.ToLower(upstreamMsg), "credit balance") {
			// Anthropic API key 余额不足（语义等同 402），停止调度
			msg := "Credit balance exhausted (400): " + upstreamMsg
			s.handleAuthError(ctx, account, msg)
			shouldDisable = true
		} else if strings.Contains(strings.ToLower(upstreamMsg), "identity verification is required") {
			// KYC 身份验证要求 → 永久禁用，账号需完成身份验证后才能恢复
			msg := "Identity verification required (400): " + upstreamMsg
			s.handleAuthError(ctx, account, msg)
			shouldDisable = true
		} else if account.Platform == PlatformOpenAI && isOpenAIImageCapabilityLoss400(statusCode, responseBody) {
			_ = s.HandleOpenAIImageCapabilityLoss400(ctx, account, statusCode, responseBody)
		} else if account.Platform == PlatformAnthropic && tkIsAnthropicClientInducedBadRequest(responseBody) {
			slog.Info("anthropic_client_induced_400_skip_penalty",
				"account_id", account.ID, "status_code", statusCode)
		}
		// 其他 400 错误（如参数问题）不处理，不禁用账号
	case 401:
		shouldDisable = s.tkHandleAuth401(ctx, account, upstreamMsg, responseBody)
	case 402:
		// 国产供应商：余额不足是可恢复状态（充值/检测恢复后由周期任务自动解除），
		// 不能走 handleAuthError 永久置 status=error。改为可恢复的临时停调。
		if account.IsCNProvider() {
			s.handleCNProviderInsufficientBalance(ctx, account, upstreamMsg)
			shouldDisable = true
			break
		}
		// OpenAI: deactivated_workspace 表示工作区已停用，直接标记 error
		if account.Platform == PlatformOpenAI && gjson.GetBytes(responseBody, "detail.code").String() == "deactivated_workspace" {
			msg := "Workspace deactivated (402): workspace has been deactivated"
			s.handleAuthError(ctx, account, msg)
			shouldDisable = true
			break
		}
		if s.tkHandleKiroQuotaLimit402(ctx, account, upstreamMsg, responseBody) {
			shouldDisable = true
			break
		}
		// 支付要求：余额不足或计费问题，停止调度
		msg := "Payment required (402): insufficient balance or billing issue"
		if upstreamMsg != "" {
			msg = "Payment required (402): " + upstreamMsg
		}
		s.handleAuthError(ctx, account, msg)
		shouldDisable = true
	case 403:
		logger.LegacyPrintf(
			"service.ratelimit",
			"[HandleUpstreamErrorRaw] account_id=%d platform=%s type=%s status=403 request_id=%s cf_ray=%s upstream_msg=%s raw_body=%s",
			account.ID,
			account.Platform,
			account.Type,
			strings.TrimSpace(headers.Get("x-request-id")),
			strings.TrimSpace(headers.Get("cf-ray")),
			upstreamMsg,
			truncateForLog(responseBody, 1024),
		)
		shouldDisable = s.handle403(ctx, account, upstreamMsg, responseBody)
	case 404:
		shouldDisable = s.handle404(ctx, account, upstreamMsg, responseBody)
	case 413:
		if account.Platform == PlatformAnthropic {
			slog.Info("anthropic_request_too_large_skip_penalty",
				"account_id", account.ID,
				"status_code", statusCode)
		}
	case 429:
		if handled, disable := s.tkTryHandle429Extras(ctx, account, statusCode, headers, upstreamMsg, responseBody); handled {
			return disable
		}
		rateLimitSet := s.handle429(ctx, account, headers, responseBody, requestedModel...)
		if account.Platform == PlatformAnthropic && tkAnthropicStubHealthFuseEligible(statusCode, rateLimitSet) {
			shouldDisable = s.handleAnthropicUpstreamErrorWithOptions(ctx, account, statusCode, upstreamMsg, responseBody, rateLimitSet)
		} else {
			shouldDisable = false
		}
	case 529:
		if customErrorCodesEnabled && account.ShouldHandleErrorCode(statusCode) {
			msg := "Custom error code triggered"
			if upstreamMsg != "" {
				msg = upstreamMsg
			}
			s.handleCustomErrorCode(ctx, account, statusCode, msg)
			shouldDisable = true
		} else {
			retryAfterOwned := s.handle529(ctx, account, headers)
			if account.Platform == PlatformAnthropic {
				shouldDisable = s.handleAnthropicUpstreamErrorWithOptions(ctx, account, statusCode, upstreamMsg, responseBody, retryAfterOwned)
			} else {
				shouldDisable = false
			}
		}
	default:
		if account.Platform == PlatformAnthropic && tkAnthropicStubHealthFuseEligible(statusCode, false) {
			shouldDisable = s.handleAnthropicUpstreamError(ctx, account, statusCode, upstreamMsg, responseBody)
		} else if customErrorCodesEnabled {
			msg := "Custom error code triggered"
			if upstreamMsg != "" {
				msg = upstreamMsg
			}
			s.handleCustomErrorCode(ctx, account, statusCode, msg)
			shouldDisable = true
		} else if statusCode >= 500 {
			// 未启用自定义错误码时：仅记录5xx错误
			slog.Warn("account_upstream_error", "account_id", account.ID, "status_code", statusCode)
			shouldDisable = false
		}
	}

	return shouldDisable
}
