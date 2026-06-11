package service

import (
	"context"
	"log/slog"
	"time"
)

// 池级全不可调度检查（事件驱动,挂在 notifyAccountSchedulingBlocked 汇聚点）。
//
// 单账号冷却走 #516 的单卡/摘要;但"最后一个可调度账号被封"是质变——整个平台
// 的流量从这一刻起全部快速失败,必须即时 P0(prod 2026-06-11 全池冷却事件中,
// 该信号本可比冷却自愈早 ~9 分钟暴露问题)。检查异步执行,不阻塞请求路径;
// 延迟一拍是为了等触发本次通知的 SetTempUnschedulable 写入提交,避免查到
// 账号自身仍可调度的假阴性。当前只对 anthropic 启用(镜像池形态最受 failover
// 扇出连锁影响);其他平台如需纳入,把 platform 判断改成集合即可。
const (
	tkPoolExhaustedCheckDelay   = 2 * time.Second
	tkPoolExhaustedCheckTimeout = 5 * time.Second
)

func (s *RateLimitService) tkCheckPlatformPoolExhausted(account *Account, until time.Time, reason string) {
	if s == nil || account == nil || s.incidentNotifier == nil || s.accountRepo == nil {
		return
	}
	if account.Platform != PlatformAnthropic {
		return
	}
	platform := account.Platform
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("pool_exhausted_check_panic_recovered", "platform", platform, "panic", r)
			}
		}()
		time.Sleep(tkPoolExhaustedCheckDelay)
		ctx, cancel := context.WithTimeout(context.Background(), tkPoolExhaustedCheckTimeout)
		defer cancel()
		s.tkPlatformPoolExhaustedCheck(ctx, platform, account, until, reason)
	}()
}

// tkPlatformPoolExhaustedCheck 是可同步调用的检查本体（拆出便于单测）。
func (s *RateLimitService) tkPlatformPoolExhaustedCheck(ctx context.Context, platform string, trigger *Account, until time.Time, reason string) {
	if s == nil || s.incidentNotifier == nil || s.accountRepo == nil || trigger == nil {
		return
	}
	accounts, err := s.accountRepo.ListSchedulableByPlatform(ctx, platform)
	if err != nil {
		slog.Warn("pool_exhausted_check_query_failed", "platform", platform, "error", err)
		return
	}
	if len(accounts) > 0 {
		return
	}
	slog.Error("platform_pool_exhausted",
		"platform", platform,
		"trigger_account_id", trigger.ID,
		"trigger_reason", reason)
	s.incidentNotifier.NotifyPlatformPoolExhausted(platform, trigger, until, reason)
}
