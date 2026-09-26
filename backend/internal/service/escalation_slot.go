package service

import (
	"context"
	"log/slog"
	"time"
)

// EscalationSlotCache 是「渐进惩罚阶梯按故障事件推进」这一守卫的共享 owner。
//
// 为什么需要它：账号的 concurrency 默认是 3，一次故障事件（Google 要求账号验证、
// 一层坏代理、上游区域性抖动）会让多个 in-flight 请求在同一瞬间拿到同样的错误。
// 阶梯若直接按「错误次数」递增，计数会在几毫秒内冲到永久禁用阈值，把中间所有
// 冷却档整体跳过——一次本可自动恢复的故障变成需要人工介入的永久禁用。
//
// 槽位把语义从「每个错误推进一轮」修正为「每个故障事件推进一轮」：
//
//	Acquire 用原子 SET-if-absent 占位，只有赢家推进阶梯并落冷却；输家直接
//	failover，不碰计数器。赢家随后把槽位 TTL 收缩到它刚落下的冷却长度，于是
//	槽位恰好在账号重新可调度的那一刻过期——冷却结束后再次命中同样的错误属于
//	新的故障事件，阶梯正常继续升级。
//
// 这个概念此前在仓库里存在多份手写拷贝，漏加槽位与清零点漂移都出现过
// （Antigravity validation 403、OpenAI 累计 403、Antigravity INTERNAL 500）。
// 新增渐进惩罚阶梯时必须复用这里，不要再写第 N 份 SETNX。
//
// 所有方法都是 best-effort：Redis 故障绝不能让守卫反过来放过真正持续失败的
// 账号，因此调用方在出错时按「照常升级」处理（见 BeginEscalationEpisode）。
type EscalationSlotCache interface {
	// AcquireEscalationSlot 原子占位，返回本次调用是否抢到槽位。
	AcquireEscalationSlot(ctx context.Context, keyPrefix string, accountID int64, ttlSeconds int) (bool, error)
	// ShrinkEscalationSlotTTL 把槽位 TTL 收缩到实际落下的冷却长度。
	ShrinkEscalationSlotTTL(ctx context.Context, keyPrefix string, accountID int64, ttlSeconds int) error
	// ReleaseEscalationSlot 释放槽位（账号恢复时调用）。
	ReleaseEscalationSlot(ctx context.Context, keyPrefix string, accountID int64) error
}

// 各阶梯的槽位 key 前缀。沿用各自迁移前已在生产使用的字符串，避免升级瞬间
// 出现「旧进程写旧 key、新进程读新 key」导致的一轮误判。
const (
	EscalationSlotPrefixAntigravityValidation  = "antigravity_validation_slot:account:"
	EscalationSlotPrefixOpenAI403              = "openai_403_escalation_slot:account:"
	EscalationSlotPrefixAntigravityInternal500 = "antigravity_internal500_escalation_slot:account:"
)

// EscalationEpisode 是一次故障事件的升级许可。零值表示「没有槽位守卫」，
// 此时 CommitCooldown 是 no-op，行为退回加槽位之前的语义。
type EscalationEpisode struct {
	cache     EscalationSlotCache
	keyPrefix string
	accountID int64
	acquired  bool
}

// BeginEscalationEpisode 尝试取得本次故障事件的升级许可。
//
// proceed=false 表示同一事件的另一个 in-flight 请求已经拿到许可并会负责落冷却，
// 调用方应当直接 failover，不要递增计数器、不要重写冷却窗口。
//
// proceed=true 表示调用方负责推进阶梯；落完冷却后必须调用
// episode.CommitCooldown(ctx, cooldown) 把槽位收缩到该冷却长度。
//
// fail open：cache 为 nil（未接线）或 Redis 出错时一律返回 proceed=true，
// 让阶梯照常升级——守卫故障绝不能放过真正持续失败的账号。
func BeginEscalationEpisode(
	ctx context.Context,
	cache EscalationSlotCache,
	keyPrefix string,
	accountID int64,
	placeholderTTL time.Duration,
) (episode EscalationEpisode, proceed bool) {
	if cache == nil || accountID <= 0 {
		return EscalationEpisode{}, true
	}

	ttlSeconds := int(placeholderTTL.Seconds())
	won, err := cache.AcquireEscalationSlot(ctx, keyPrefix, accountID, ttlSeconds)
	if err != nil {
		slog.Warn("escalation_slot_acquire_failed",
			"key_prefix", keyPrefix, "account_id", accountID, "error", err)
		return EscalationEpisode{}, true
	}
	if !won {
		slog.Info("escalation_suppressed_same_episode",
			"key_prefix", keyPrefix, "account_id", accountID)
		return EscalationEpisode{}, false
	}

	return EscalationEpisode{
		cache:     cache,
		keyPrefix: keyPrefix,
		accountID: accountID,
		acquired:  true,
	}, true
}

// CommitCooldown 把槽位 TTL 收缩到本轮实际落下的冷却长度，使槽位恰好在账号
// 重新可调度时过期。失败仅损失一次升级精度（槽位按占位 TTL 过期，偏保守地
// 多抑制一段时间的升级），不影响当前请求处理。
func (e EscalationEpisode) CommitCooldown(ctx context.Context, cooldown time.Duration) {
	if !e.acquired || e.cache == nil {
		return
	}
	if err := e.cache.ShrinkEscalationSlotTTL(ctx, e.keyPrefix, e.accountID, int(cooldown.Seconds())); err != nil {
		slog.Warn("escalation_slot_shrink_failed",
			"key_prefix", e.keyPrefix,
			"account_id", e.accountID,
			"cooldown_seconds", int(cooldown.Seconds()),
			"error", err)
	}
}

// ReleaseEscalationSlot 释放槽位。账号恢复（成功响应 / 人工清理）后必须调用：
// 残留槽位会把下一次真实故障事件误判成同一轮，静默跳过该落的冷却。
func ReleaseEscalationSlot(
	ctx context.Context, cache EscalationSlotCache, keyPrefix string, accountID int64,
) {
	if cache == nil || accountID <= 0 {
		return
	}
	if err := cache.ReleaseEscalationSlot(ctx, keyPrefix, accountID); err != nil {
		slog.Warn("escalation_slot_release_failed",
			"key_prefix", keyPrefix, "account_id", accountID, "error", err)
	}
}
