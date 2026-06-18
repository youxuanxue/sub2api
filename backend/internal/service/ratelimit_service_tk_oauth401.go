package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// TK：OAuth 账号 401 的「grant 被上游吊销」判定——单一信号，第一次即禁。
//
// 判据只有一个布尔：401 发生时 access_token 是否仍然有效（离过期还有足够余量）。
//   - 仍有效 → 一个没过期的 token 被上游 401，正常不该发生，除非 grant 已被吊销：
//     第一次即 SetError 永久停调度 + 告警（提示人工重授权），不再走 temp_unschedulable 冷却。
//   - 已过期 / 在刷新窗口内 → 过期抢跑（请求拿旧 token 抢在后台刷新前打上游）的良性 401：
//     不禁用，回退调用方的 temp_unschedulable 冷却 + 后台刷新自愈。
//
// 取代旧的双触发计数器（version-bump + same-version + 窗口 + debounce）。依据 2026-06 prod
// 7 天全队取证：真实吊销 401 的 access_token 全部仍有效（旧 same-version 特征），过期抢跑型
// 良性 401 = 0。token 有效性这一个布尔同时覆盖两件事：① edge-uk1 2026-06-08 那类「有效但被
// 吊销、版本冻结、永不刷新」的无限 flap（旧逻辑要跨冷却周期才升级，这里第一次即禁，更快）；
// ② 不把过期抢跑 401 误判为永久吊销（旧『naive 401 计数器』的反面教材）。
//
// fail-safe：expires_at 缺失/不可解析（无法确认有效性）→ 返回 false 回退冷却，绝不在
// 「不确定」时永久禁用账号。

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
	// （Claude API 故障期间对全队的统一豁免在 HandleUpstreamError 的 case 401 顶部、
	// 进入本函数之前就拦截了，见 ratelimit_service.go oauth_401_disable_deferred_during_incident。）
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

// tkApplyOAuth401Cooldown 把一次 OAuth 401 落成 temp_unschedulable 冷却（替代永久禁用）+
// 发调度阻塞通知。供两处复用，确保冷却时长/通知口径一致：
//   - HandleUpstreamError case 401 的「普通 401 回退」分支；
//   - 同处「Claude API 故障期间统一豁免」的顶层 gate。
//
// reasonMsg 写入 temp_unschedulable_reason。
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
