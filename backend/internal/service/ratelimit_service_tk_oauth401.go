package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// TK：非 Anthropic OAuth 账号 401 的「grant 被上游吊销」判定。
//
// Anthropic OAuth 已在 HandleUpstreamError case 401 顶层统一 SetError（不区分 token
// 是否过期）。本文件仅服务 OpenAI / Gemini 等其它 OAuth 平台。

// oauth401ValidTokenMarginFloor：判定 access_token「仍然有效」所需剩余有效期的下限兜底。
// 实际余量 = max(本下限, 刷新窗口)；下限防 refresh_before_expiry_hours 误配成 0 时退化成
// 「任意非过期 token 即判吊销」。
const oauth401ValidTokenMarginFloor = 5 * time.Minute

// tkOAuth401ValidTokenMargin 返回判定 token「solidly valid」所需的剩余有效期余量：
// max(刷新窗口, 下限)。刷新窗口 = token_refresh.refresh_before_expiry_hours 小时——与后台
// 刷新服务（ClaudeTokenRefresher.NeedsRefresh）同一口径：落在刷新窗口内的近过期 token 本应
// 已被刷新，其 401 归入良性抢跑而非吊销。
func (s *RateLimitService) tkOAuth401ValidTokenMargin() time.Duration {
	margin := oauth401ValidTokenMarginFloor
	if s != nil && s.cfg != nil {
		if w := time.Duration(s.cfg.TokenRefresh.RefreshBeforeExpiryHours * float64(time.Hour)); w > margin {
			margin = w
		}
	}
	return margin
}

// tkDisableIfOAuth401OnValidToken：若本次 OAuth 401 发生在一个仍然有效的 access_token 上
// （剩余有效期 ≥ tkOAuth401ValidTokenMargin），判定 grant 被上游吊销，第一次即 SetError
// 永久停调度 + 告警，返回 true（调用方应 break，不再走冷却）。token 已过期/近过期/expires_at
// 不可解析时返回 false，调用方回退 temp_unschedulable 冷却。
func (s *RateLimitService) tkDisableIfOAuth401OnValidToken(ctx context.Context, account *Account, upstreamMsg string) bool {
	if s == nil || account == nil {
		return false
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		// 无法确认有效性 → fail-safe，不永久禁用，回退冷却。
		slog.Warn("oauth_401_expiry_unknown_fallback_cooldown",
			"account_id", account.ID, "platform", account.Platform)
		return false
	}
	// token 已过期 / 在刷新窗口内 → 过期抢跑良性 401，回退冷却 + 后台刷新自愈。
	if time.Until(*expiresAt) < s.tkOAuth401ValidTokenMargin() {
		return false
	}
	// token 仍 solidly valid 却被上游 401 → grant 吊销 → 第一次即禁用，人工重授权。
	msg := "OAuth 401 on a still-valid access token — grant revoked upstream, manual re-authorization required (re-login via account management)"
	if strings.TrimSpace(upstreamMsg) != "" {
		msg = msg + ": " + upstreamMsg
	}
	// greppable marker（§8.5 排障约定）。
	slog.Warn("oauth_401_valid_token_revoked",
		"account_id", account.ID,
		"platform", account.Platform,
		"expires_at", expiresAt.UTC().Format(time.RFC3339),
	)
	s.handleAuthError(ctx, account, msg)
	return true
}

// tkApplyOAuth401Cooldown 把一次非-Anthropic OAuth 401 落成 temp_unschedulable 冷却 +
// 发调度阻塞通知。
func (s *RateLimitService) tkApplyOAuth401Cooldown(ctx context.Context, account *Account, reasonMsg string) {
	cooldownMinutes := s.cfg.RateLimit.OAuth401CooldownMinutes
	if cooldownMinutes <= 0 {
		cooldownMinutes = 10
	}
	until := time.Now().Add(time.Duration(cooldownMinutes) * time.Minute)
	s.notifyAccountSchedulingBlocked(account, until, "oauth_401")
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reasonMsg); err != nil {
		slog.Warn("oauth_401_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
	}
}
