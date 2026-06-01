package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// TK：OAuth「refresh 端点成功却仍持续 401」静默 flap 盲区的升级逻辑。
//
// 背景：当 OAuth 账号的 grant 被上游实质吊销，但 refresh 端点仍返回成功（换出新
// access_token，可新 token 拿去打上游依然 401 Invalid authentication credentials）时，
// 网关 401 处理只会 SetTempUnschedulable（冷却 10min），后台刷新又因 refresh 本身成功
// 而清掉冷却 → 账号复活 → 用新 token 再 401，二者谁都不升级到 error，账号在
// active ⇄ temp_unschedulable 之间无限 flap，不报 error、不告警。本文件检测到该模式后
// 升级为 error 永久停调度 + 告警，提示需手工重新授权。

const (
	// oauth401AfterRefreshDisableThresholdDefault：token 版本闸门已过滤掉「过期待刷新 /
	// 并发同 token 突发 / 首次 401」，故默认 1——第一次 401 种 baseline，其后一次版本
	// 递增的 401（=一个完整 flap 周期）即判定 grant 被吊销并升级。
	oauth401AfterRefreshDisableThresholdDefault = 1
	// oauth401AfterRefreshWindowMinutesDefault：baseline token 版本的存活窗口；跨窗口的
	// 401 重新种 baseline 而非升级，防止几小时前的良性瞬时 401 与今天一次新瞬时 401
	// 凑成误升级。
	oauth401AfterRefreshWindowMinutesDefault = 60
)

// SetOAuth401AfterRefreshCounter 设置「refresh 后仍 401」计数器（可选依赖；未注入时
// tkTryEscalateRevokedOAuth401 直接回退到既有 temp_unschedulable 冷却）。
func (s *RateLimitService) SetOAuth401AfterRefreshCounter(cache OAuth401AfterRefreshCounterCache) {
	s.oauth401AfterRefreshCounter = cache
}

// getOAuth401AfterRefreshThreshold 返回配置的升级阈值，未设 / 零 / 负回退到默认 1。
func (s *RateLimitService) getOAuth401AfterRefreshThreshold() int64 {
	if s != nil && s.cfg != nil && s.cfg.RateLimit.OAuth401AfterRefreshDisableThreshold > 0 {
		return int64(s.cfg.RateLimit.OAuth401AfterRefreshDisableThreshold)
	}
	return oauth401AfterRefreshDisableThresholdDefault
}

// getOAuth401AfterRefreshWindowMinutes 返回 baseline 版本存活窗口，未设 / 零 / 负回退默认 60。
func (s *RateLimitService) getOAuth401AfterRefreshWindowMinutes() int {
	if s != nil && s.cfg != nil && s.cfg.RateLimit.OAuth401AfterRefreshWindowMinutes > 0 {
		return s.cfg.RateLimit.OAuth401AfterRefreshWindowMinutes
	}
	return oauth401AfterRefreshWindowMinutesDefault
}

// ResetOAuth401AfterRefreshCounter 在账号成功响应 / 恢复后清零计数与 baseline 版本，
// 避免一次良性瞬时 401 的 baseline 长期残留累积。计数器未注入时为 no-op。
func (s *RateLimitService) ResetOAuth401AfterRefreshCounter(ctx context.Context, accountID int64) {
	if s == nil || s.oauth401AfterRefreshCounter == nil || accountID <= 0 {
		return
	}
	if err := s.oauth401AfterRefreshCounter.ResetOAuth401AfterRefresh(ctx, accountID); err != nil {
		slog.Warn("oauth_401_after_refresh_reset_failed", "account_id", accountID, "error", err)
	}
}

// tkTryEscalateRevokedOAuth401 判定本次 OAuth 401 是否属于「token 已被成功刷新过却仍
// 401」（grant 实质吊销）的 flap，并在达阈值时升级为 error 永久停调度 + 告警。
// 返回 true 表示已升级（调用方应 break，不再走 temp_unschedulable 冷却）。
//
// 闸门：用账号请求快照里 credentials["_token_version"]（每次成功 refresh 盖一个
// UnixMilli 戳）比对上一次 401 记录的版本——只有版本递增（其间确实换了新 token）才计数。
//
// 任一前置不满足（计数器未注入 / _token_version 缺失 / 计数器返回错误）时返回 false，
// 回退到既有 temp_unschedulable 冷却。这是 fail-open：绝不因计数器缺失或故障把账号误判
// 为永久禁用。
func (s *RateLimitService) tkTryEscalateRevokedOAuth401(ctx context.Context, account *Account, upstreamMsg string) bool {
	if s == nil || s.oauth401AfterRefreshCounter == nil || account == nil {
		return false
	}
	// _token_version 缺失 / 非正（极老数据从未刷新过）→ 无法判断是否换过新 token，保守回退。
	tokenVersion := account.GetCredentialAsInt64("_token_version")
	if tokenVersion <= 0 {
		return false
	}

	count, err := s.oauth401AfterRefreshCounter.RecordOAuth401AfterRefresh(
		ctx, account.ID, tokenVersion, s.getOAuth401AfterRefreshWindowMinutes(),
	)
	if err != nil {
		slog.Warn("oauth_401_after_refresh_record_failed", "account_id", account.ID, "error", err)
		return false
	}

	threshold := s.getOAuth401AfterRefreshThreshold()
	if count < threshold {
		return false
	}

	msg := fmt.Sprintf(
		"OAuth 401 persists after %d successful token refresh(es) — refresh_token likely revoked, manual re-authorization required (re-login via account management)",
		count,
	)
	if strings.TrimSpace(upstreamMsg) != "" {
		msg = msg + ": " + upstreamMsg
	}
	// greppable marker（§8.5 排障约定）。handleAuthError 内部已 notify 告警 + SetError。
	slog.Warn("oauth_401_after_refresh_revoked",
		"account_id", account.ID,
		"platform", account.Platform,
		"count", count,
		"threshold", threshold,
		"token_version", tokenVersion,
	)
	s.handleAuthError(ctx, account, msg)
	return true
}
