package service

import (
	"context"
	"log/slog"
	"strings"
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

	// tkPoolExhaustedTransientCooldown 是「瞬时 drain vs 持续故障」的判别阈值。
	// Anthropic 冷却阶梯 anthropicCooldownTierLadder=[30s,2m,10m]:单次 529/503 走
	// tier-0(~30s)秒级自愈;**持续** 529/503 才经 3/3 升级进 tier-1(2m)/tier-2(10m)。
	// 60s 卡在 tier-0 与 tier-1 之间——本次冷却剩余 ≤60s = 单次瞬时(去噪),>60s = 账号
	// 已因连续错误升级 = 真持续故障(P0)。
	tkPoolExhaustedTransientCooldown = 60 * time.Second
)

// tkPoolExhaustedEnabled 报告某平台是否启用"平台池全不可调度"即时 P0 检查。
//
// 从硬编码白名单(anthropic/openai/gemini)改为派生:任何非空平台都纳入。平台名
// 直接来自被封账号自身(notifyAccountSchedulingBlocked),不该枚举——新平台
// (kiro/grok/antigravity/newapi…)一上线就自动有空池火警,零代码改动。触发受三重收口:
// 「该平台 ListSchedulableByPlatform 真为 0」+「本次冷却**非瞬时**(剩余 > 60s,即连续
// 529/503 已经 3/3 升级到 tier-1/2,而非单次 tier-0 自愈)」+ 每平台 10min 去重。
// **瞬时** drain(单次上游冷却秒级自愈,单账号 edge 上拓扑保证会反复响)降级为
// platform_pool_transient_drain WARN,不再 P0;**持续** 529/503(账号已升级到长冷却,含
// 2026-06-11 七号同倒的 10min 全池崩塌)仍即时 P0——见 tkPlatformPoolExhaustedCheck。
//
// 注意 newapi 是「一个平台、多互不可替代上游」:这条平台级火警只在 newapi 所有
// channel 全空时才响(粗粒度兜底);单 channel 的容量饱和由 pool_load_rate 指标按
// channel_type 分别覆盖(前瞻),单 channel 彻底死号由单账号永久失效 P0 覆盖。
func tkPoolExhaustedEnabled(platform string) bool {
	return strings.TrimSpace(platform) != ""
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
	// 「瞬时 vs 持续」降噪(承接 2026-06-18 决策:瞬时去掉、连续保留)。判别用本次冷却
	// 的剩余时长:单次 529/503 走 tier-0(~30s)秒级自愈——单账号 edge 上这种 drain 是
	// 拓扑保证会反复响的噪声(实测 edge-us7 2026-06-18: 30s 冷却、857:1 服务比、1 请求
	// 受影响,prod→edge 镜像中继已 failover 同级兄弟 edge 承接),不该 P0。**持续** 529/503
	// 才会经 3/3 阶梯升级进 tier-1(2m)/tier-2(10m),届时剩余时长 > 60s = 账号因连续错误
	// 升级 = 真持续故障(含 2026-06-11 七号同倒的 10min 全池崩塌),必须 P0。
	// until 缺失(零值)= 无法判定瞬时 → 保守 P0(宁可多报不可漏报)。
	//
	// 不变量(降噪安全性依据):until 是**触发本次空池的那个账号**的冷却终点,而正是
	// 这个账号会在 until 时刻把池重新填上——所以剩余短 ⇒ 池近期必自愈,抑制安全;池里
	// 若还有别的账号在更长冷却也不延长空窗(本账号先回来扛流量)。若本账号到点又立刻
	// 失败,error_count 累加触发阶梯升级,下一次触发的 until 就 > 60s → 照常 P0。即使别的
	// 账号反而更早到点(本账号冷却长),那也只是 until 高估空窗 → 偏向多报,不会漏报。
	remaining := time.Until(until)
	if !until.IsZero() && remaining <= tkPoolExhaustedTransientCooldown {
		slog.Warn("platform_pool_transient_drain",
			"platform", platform,
			"trigger_account_id", trigger.ID,
			"trigger_reason", reason,
			"cooldown_remaining_seconds", int(remaining.Seconds()))
		return
	}
	slog.Error("platform_pool_exhausted",
		"platform", platform,
		"trigger_account_id", trigger.ID,
		"trigger_reason", reason)
	s.incidentNotifier.NotifyPlatformPoolExhausted(platform, trigger, until, reason)
}
