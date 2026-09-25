package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// applyAntigravityValidationPenalty 对 Antigravity VALIDATION_REQUIRED (403)
// 应用渐进惩罚，语义对齐同平台的 INTERNAL 500 阶梯
// (antigravity_internal500_penalty.go) 与 OpenAI 403 阶梯：
//
//	第 1 轮 → 临时停调 30 分钟
//	第 2 轮 → 临时停调 2 小时
//	第 3 轮 → SetError 永久禁用，等人工验证
//
// 计数器缺失（未接线或 Redis 不可用）时退回单档固定冷却，保持修复前的行为，
// 绝不因为缺少可选依赖就把可恢复的验证挑战升级成永久禁用。
//
// 返回值沿用 handleAntigravity403 的约定：true = 当前请求必须 failover。
func (s *RateLimitService) applyAntigravityValidationPenalty(
	ctx context.Context, account *Account, msg string,
) (shouldDisable bool) {
	if s.antigravityValidationCounter == nil {
		s.cooldownAntigravityValidation(ctx, account, msg, antigravityValidationCooldownLadder[0], 0)
		return true
	}

	count, err := s.antigravityValidationCounter.IncrementAntigravityValidationCount(
		ctx, account.ID, antigravityValidationCounterWindowMinutes,
	)
	if err != nil {
		// 计数失败不改变可恢复语义，只退回首档冷却。
		slog.Warn("antigravity_validation_403_increment_failed", "account_id", account.ID, "error", err)
		s.cooldownAntigravityValidation(ctx, account, msg, antigravityValidationCooldownLadder[0], 0)
		return true
	}

	if count >= int64(antigravityValidationDisableThreshold) {
		disableMsg := fmt.Sprintf("%s | consecutive_validation_403=%d/%d",
			msg, count, antigravityValidationDisableThreshold)
		s.handleAuthError(ctx, account, disableMsg)
		slog.Warn("antigravity_validation_403_account_disabled",
			"account_id", account.ID,
			"account_name", account.Name,
			"consecutive_count", count,
			"threshold", antigravityValidationDisableThreshold,
		)
		return true
	}

	tier := int(count) - 1
	if tier < 0 {
		tier = 0
	}
	if tier >= len(antigravityValidationCooldownLadder) {
		tier = len(antigravityValidationCooldownLadder) - 1
	}
	s.cooldownAntigravityValidation(ctx, account, msg, antigravityValidationCooldownLadder[tier], count)
	return true
}

// cooldownAntigravityValidation 落一次临时停调。持久化失败时 fail closed，
// 避免账号在紧循环里被反复重试。
func (s *RateLimitService) cooldownAntigravityValidation(
	ctx context.Context, account *Account, msg string, cooldown time.Duration, count int64,
) {
	until := time.Now().Add(cooldown)
	reason := "Antigravity validation required temporary cooldown: " + msg
	if count > 0 {
		reason = fmt.Sprintf("Antigravity validation required temporary cooldown (%d/%d): %s",
			count, antigravityValidationDisableThreshold, msg)
	}
	s.notifyAccountSchedulingBlocked(account, until, "antigravity_validation_403")
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("antigravity_validation_403_set_temp_unschedulable_failed",
			"account_id", account.ID, "error", err)
		// If the cooldown cannot be persisted, fail closed so the account is
		// not retried in a tight loop.
		s.handleAuthError(ctx, account, msg)
		return
	}
	slog.Warn("antigravity_validation_403_temp_unschedulable",
		"account_id", account.ID,
		"until", until,
		"cooldown", cooldown,
		"consecutive_count", count,
	)
}

// ResetAntigravityValidationCounter 清零 VALIDATION_REQUIRED 连续命中计数。
// 由成功响应与人工恢复路径调用：验证挑战一旦真正解除，账号不应带着旧轮数
// 进入下一次冷却阶梯。
func (s *RateLimitService) ResetAntigravityValidationCounter(ctx context.Context, accountID int64) {
	if s == nil || s.antigravityValidationCounter == nil || accountID <= 0 {
		return
	}
	if err := s.antigravityValidationCounter.ResetAntigravityValidationCount(ctx, accountID); err != nil {
		slog.Warn("antigravity_validation_403_reset_failed", "account_id", accountID, "error", err)
	}
}
