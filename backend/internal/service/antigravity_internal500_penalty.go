package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/tidwall/gjson"
)

// INTERNAL 500 渐进惩罚：连续多轮全部返回特定 500 错误时的惩罚时长
const (
	internal500PenaltyTier1Duration  = 30 * time.Minute // 第 1 轮：临时不可调度 30 分钟
	internal500PenaltyTier2Duration  = 2 * time.Hour    // 第 2 轮：临时不可调度 2 小时
	internal500PenaltyTier3Threshold = 3                // 第 3+ 轮：永久禁用
)

// isAntigravityInternalServerError 检测特定的 INTERNAL 500 错误
// 必须同时匹配 error.code==500, error.message=="Internal error encountered.", error.status=="INTERNAL"
func isAntigravityInternalServerError(statusCode int, body []byte) bool {
	if statusCode != http.StatusInternalServerError {
		return false
	}
	return gjson.GetBytes(body, "error.code").Int() == 500 &&
		gjson.GetBytes(body, "error.message").String() == "Internal error encountered." &&
		gjson.GetBytes(body, "error.status").String() == "INTERNAL"
}

// applyInternal500Penalty 根据连续 INTERNAL 500 轮次数应用渐进惩罚
// count=1: temp_unschedulable 30 分钟（internal500PenaltyTier1Duration）
// count=2: temp_unschedulable 2 小时（internal500PenaltyTier2Duration）
// count>=3: SetError 永久禁用（internal500PenaltyTier3Threshold）
func (s *AntigravityGatewayService) applyInternal500Penalty(
	ctx context.Context, prefix string, account *Account, count int64,
) {
	switch {
	case count >= int64(internal500PenaltyTier3Threshold):
		reason := fmt.Sprintf("INTERNAL 500 consecutive failures: %d rounds", count)
		if err := s.accountRepo.SetError(ctx, account.ID, reason); err != nil {
			slog.Error("internal500_set_error_failed", "account_id", account.ID, "error", err)
			return
		}
		slog.Warn("internal500_account_disabled",
			"account_id", account.ID, "account_name", account.Name, "consecutive_count", count)
	case count == 2:
		until := time.Now().Add(internal500PenaltyTier2Duration)
		reason := fmt.Sprintf("INTERNAL 500 x%d (temp unsched %v)", count, internal500PenaltyTier2Duration)
		if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
			slog.Error("internal500_temp_unsched_failed", "account_id", account.ID, "error", err)
			return
		}
		slog.Warn("internal500_temp_unschedulable",
			"account_id", account.ID, "account_name", account.Name,
			"duration", internal500PenaltyTier2Duration, "consecutive_count", count)
	case count == 1:
		until := time.Now().Add(internal500PenaltyTier1Duration)
		reason := fmt.Sprintf("INTERNAL 500 x%d (temp unsched %v)", count, internal500PenaltyTier1Duration)
		if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
			slog.Error("internal500_temp_unsched_failed", "account_id", account.ID, "error", err)
			return
		}
		slog.Info("internal500_temp_unschedulable",
			"account_id", account.ID, "account_name", account.Name,
			"duration", internal500PenaltyTier1Duration, "consecutive_count", count)
	}
}

// handleInternal500RetryExhausted 处理 INTERNAL 500 重试耗尽：递增计数器并应用惩罚
func (s *AntigravityGatewayService) handleInternal500RetryExhausted(
	ctx context.Context, prefix string, account *Account,
) {
	if s.internal500Cache == nil {
		return
	}

	// 同一故障事件只推进一轮：Google 侧区域性故障时，多个并发请求会各自跑完
	// 3 轮重试且全部命中 INTERNAL 500，各自递增计数。没有守卫时计数冲到
	// internal500PenaltyTier3Threshold，30min / 2h 两档冷却被整体跳过、账号被
	// 直接永久禁用。守卫由共享的 EscalationSlotCache 持有（escalation_slot.go）。
	episode, proceed := BeginEscalationEpisode(
		ctx, s.escalationSlots, EscalationSlotPrefixAntigravityInternal500,
		account.ID, internal500EscalationSlotPlaceholder,
	)
	if !proceed {
		return
	}

	count, err := s.internal500Cache.IncrementInternal500Count(ctx, account.ID)
	if err != nil {
		slog.Error("internal500_counter_increment_failed",
			"prefix", prefix, "account_id", account.ID, "error", err)
		return
	}
	s.applyInternal500Penalty(ctx, prefix, account, count)
	episode.CommitCooldown(ctx, internal500CooldownForCount(count))
}

// internal500CooldownForCount 返回某一轮实际落下的冷却长度，用于把升级槽位
// 收缩到账号重新可调度的时刻。分支结构与 applyInternal500Penalty 逐档对齐，
// 改那边的档位时这里必须同步（TestInternal500CooldownForCount_对齐实际落下的冷却档
// 锚住这个一致性）。
//
// 头两个分支返回同一个时长但语义不同，故不合并：永久禁用档根本没有冷却终点，
// 此时账号已被 SetError，槽位长短不再影响调度，沿用最长一档只是取个安全值；
// count==2 才是真的落 2 小时冷却。合并会让这个对应关系消失。
func internal500CooldownForCount(count int64) time.Duration {
	switch {
	case count >= int64(internal500PenaltyTier3Threshold):
		// 永久禁用：无冷却终点，取最长档作安全值。
		return internal500PenaltyTier2Duration
	case count == 2:
		// 实际落 internal500PenaltyTier2Duration 冷却。
		return internal500PenaltyTier2Duration
	default:
		// 实际落 internal500PenaltyTier1Duration 冷却。
		return internal500PenaltyTier1Duration
	}
}

// internal500EscalationSlotPlaceholder 是槽位占位 TTL：取阶梯最长冷却档，
// 保证「抢到槽位但收缩失败」时宁可多抑制一轮升级，也不把一次故障事件拆成
// 多轮推向永久禁用。
var internal500EscalationSlotPlaceholder = internal500PenaltyTier2Duration

// resetInternal500Counter 成功响应时清零 INTERNAL 500 计数器
func (s *AntigravityGatewayService) resetInternal500Counter(
	ctx context.Context, prefix string, accountID int64,
) {
	// 账号恢复后必须一并释放升级槽位：残留槽位会把下一次真实故障事件误判成
	// 同一轮，静默跳过该落的冷却。
	ReleaseEscalationSlot(ctx, s.escalationSlots, EscalationSlotPrefixAntigravityInternal500, accountID)
	if s.internal500Cache == nil {
		return
	}
	if err := s.internal500Cache.ResetInternal500Count(ctx, accountID); err != nil {
		slog.Error("internal500_counter_reset_failed",
			"prefix", prefix, "account_id", accountID, "error", err)
	}
}
