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
// 账号自身仍可调度的假阴性。
const (
	tkPoolExhaustedCheckDelay   = 2 * time.Second
	tkPoolExhaustedCheckTimeout = 5 * time.Second
)

// tkPoolExhaustedPlatforms 是启用"平台池全不可调度"即时 P0 检查的平台集合。
// anthropic 是镜像池形态、最受 failover 扇出连锁影响(prod 2026-06-11);
// openai(gpt)/gemini(google) 同为有专属账号池的平台——号池被打满同样让该平台
// 流量从那一刻起全部快速失败(429,见线上 2026-06-17 反馈"gpt 号池满了"),
// 纳入同一即时 P0 通道。其他平台(newapi/kiro/grok/antigravity)如需纳入,
// 加进本集合即可。
var tkPoolExhaustedPlatforms = map[string]struct{}{
	PlatformAnthropic: {},
	PlatformOpenAI:    {},
	PlatformGemini:    {},
}

// tkPoolExhaustedEnabled 报告某平台是否启用池级全不可调度即时 P0 检查。
func tkPoolExhaustedEnabled(platform string) bool {
	_, ok := tkPoolExhaustedPlatforms[platform]
	return ok
}

func (s *RateLimitService) tkCheckPlatformPoolExhausted(account *Account, until time.Time, reason string) {
	if s == nil || account == nil || s.incidentNotifier == nil || s.accountRepo == nil {
		return
	}
	if !tkPoolExhaustedEnabled(account.Platform) {
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
