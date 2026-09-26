package service

import "context"

// AntigravityValidationCounterCache 追踪 Antigravity 账号连续命中 Google
// VALIDATION_REQUIRED (403) 挑战的轮数。
//
// 没有这个计数器时，需要人工验证的账号会以固定冷却周期被反复放回调度池，
// 每轮消耗一次真实上游请求再被打下去；计数器让冷却随轮数升级，并在成功
// 响应后清零。
type AntigravityValidationCounterCache interface {
	// IncrementAntigravityValidationCount 原子递增计数并返回当前值。
	IncrementAntigravityValidationCount(ctx context.Context, accountID int64, windowMinutes int) (int64, error)
	// ResetAntigravityValidationCount 清零计数器（成功响应/人工恢复时调用）。
	ResetAntigravityValidationCount(ctx context.Context, accountID int64) error

	// 升级槽位（escalation slot）让阶梯按「验证事件」而不是「错误次数」推进，
	// 语义与 AnthropicUpstreamErrorCounterCache 的同名槽位一致（issue #623）。
	//
	// 账号的 concurrency 默认是 3，同一个 VALIDATION_REQUIRED 事件会让 3 个
	// in-flight 请求同时拿到 403。没有槽位时每个请求各自 INCR 一次，计数在几
	// 毫秒内冲到 3，直接跳过 30 分钟和 2 小时两档冷却把账号永久禁用——而这
	// 只是一次验证挑战，本该 30 分钟后自动重试。
	//
	// AcquireAntigravityValidationEscalationSlot 用原子 SET-if-absent 占位，返回
	// 本次调用是否抢到槽位。赢家递增计数并落冷却，随后用
	// SetAntigravityValidationEscalationSlotTTL 把槽位收缩到刚落下的冷却长度，
	// 于是槽位恰好在账号重新可调度时过期：冷却结束后再次命中 403 是新的一轮，
	// 阶梯正常升级。输家直接 failover，不推进计数。
	//
	// 两者都是 best-effort：Redis 故障时调用方按「不抢到也照常升级」处理，
	// 绝不让守卫失效导致真正持续失败的账号被放过。
	AcquireAntigravityValidationEscalationSlot(ctx context.Context, accountID int64, ttlSeconds int) (bool, error)
	SetAntigravityValidationEscalationSlotTTL(ctx context.Context, accountID int64, ttlSeconds int) error
	ResetAntigravityValidationEscalationSlot(ctx context.Context, accountID int64) error
}
