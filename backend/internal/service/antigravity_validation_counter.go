package service

import "context"

// AntigravityValidationCounterCache 追踪 Antigravity 账号连续命中 Google
// VALIDATION_REQUIRED (403) 挑战的轮数。
//
// 没有这个计数器时，需要人工验证的账号会以固定冷却周期被反复放回调度池，
// 每轮消耗一次真实上游请求再被打下去；计数器让冷却随轮数升级，并在成功
// 响应后清零。
//
// 「同一故障事件只推进一轮」的并发守卫不在这里：它由共享的
// EscalationSlotCache 持有（escalation_slot.go），本阶梯通过
// EscalationSlotPrefixAntigravityValidation 复用。
type AntigravityValidationCounterCache interface {
	// IncrementAntigravityValidationCount 原子递增计数并返回当前值。
	IncrementAntigravityValidationCount(ctx context.Context, accountID int64, windowMinutes int) (int64, error)
	// ResetAntigravityValidationCount 清零计数器（成功响应/人工恢复时调用）。
	ResetAntigravityValidationCount(ctx context.Context, accountID int64) error
}
