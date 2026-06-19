package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// 平台池全不可调度 → 即时 P0 飞书卡片。
//
// 事件背景（prod 2026-06-11）：7 个 anthropic 镜像账号在一分钟内被同一个"毒请求"
// 的 failover 扇出连锁封进 10 分钟冷却,06:49 起全池空转、用户侧 429,而既有
// #516 体系只看单账号事件、anthropic_cooldown_tier_escalation_count 评估器
// 也抓不到"全池同时倒"这个形态——冷却自愈(06:58)比任何人发现都早。
// 本卡片在"最后一个可调度账号被封"的那一刻即时发出（事件驱动,非轮询）,
// 把发现时间从 ~10 分钟压到秒级。
const accountIncidentPoolExhaustedDedupeWindow = 10 * time.Minute

// 池恢复轮询：空池火警(NotifyPlatformPoolExhausted)只解决「发现」,不解决「闭环」——
// 运维收到 P0 后开始补号,却没有信号告诉他「池已经回来了,可以停手」。最常见的恢复路径
// 是**冷却到期自愈**(压垮池的那个账号 until 到点自动重新可调度),而到期是被动的、不触发
// 任何事件,所以恢复无法事件驱动,只能轮询。这条轮询只在「确有平台处于空池告警态」时才查库
// (poolExhaustSentAt 非空),健康常态下零 DB 负担。恢复与空池火警严格 1:1 配对:只有发过红卡
// 的平台才会发对应绿卡,发后即从台账摘除——天然杜绝独立绿卡刷屏。
const (
	poolRecoveryCheckInterval = 30 * time.Second
	poolRecoveryCheckTimeout  = 5 * time.Second
)

// SetPoolSchedulableCounter 注入「某平台当前可调度账号数」查询(由 RateLimitService 提供)。
// 在 Start() 前调用;nil 时池恢复轮询降级为 no-op。
func (n *TKAccountIncidentNotifier) SetPoolSchedulableCounter(fn func(ctx context.Context, platform string) (int, error)) {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.poolSchedulableCounter = fn
	n.mu.Unlock()
}

// NotifyPlatformPoolExhausted 发送平台池全不可调度 P0 卡片。按 platform 去重
// （冷却风暴里每个后续账号封锁都会再触发一次池级检查,只发第一张）。
func (n *TKAccountIncidentNotifier) NotifyPlatformPoolExhausted(platform string, trigger *Account, until time.Time, reason string) {
	if n == nil || strings.TrimSpace(platform) == "" {
		return
	}
	now := n.currentTime()
	n.mu.Lock()
	if n.poolExhaustSentAt == nil {
		n.poolExhaustSentAt = map[string]time.Time{}
	}
	if last, seen := n.poolExhaustSentAt[platform]; seen && now.Sub(last) < accountIncidentPoolExhaustedDedupeWindow {
		n.mu.Unlock()
		return
	}
	n.poolExhaustSentAt[platform] = now
	n.mu.Unlock()

	title := fmt.Sprintf("TokenKey 平台池全不可调度 [%s]", n.siteID)
	body := buildPoolExhaustedText(n.siteID, platform, trigger, until, reason, now)
	n.send(title, "red", body, fmt.Sprintf("platform=%s reason=%s", platform, reason))
}

func buildPoolExhaustedText(site, platform string, trigger *Account, until time.Time, reason string, now time.Time) string {
	triggerLabel := "-"
	if trigger != nil {
		triggerLabel = accountIncidentLabel(trigger)
	}
	untilText := "未知"
	if !until.IsZero() {
		untilText = formatAlertTime(until)
	}
	return fmt.Sprintf("**节点**：%s\n**平台**：%s\n**事件**：该平台可调度账号数已降为 0,所有流量将快速失败(429)\n**压垮池的最后账号**：%s\n**该账号 reason**：%s\n**该账号冷却至**：%s\n**时间**：%s\n\n**建议**：%s",
		escapeFeishuText(site),
		escapeFeishuText(strings.TrimSpace(platform)),
		escapeFeishuText(triggerLabel),
		escapeFeishuText(strings.TrimSpace(reason)),
		escapeFeishuText(untilText),
		escapeFeishuText(formatAlertTime(now)),
		// 建议文本与既有非转义字面量同形(含 / 路径),不过 escapeFeishuText。
		poolExhaustedAdvice(platform),
	)
}

// poolExhaustedAdvice 按平台给出"池全空"时的处置建议。anthropic 是 prod→edge
// 镜像中继形态,排障先看各 edge 自身健康 + failover 扇出连锁(2026-06-11);而
// openai(gpt)/gemini(google) 等是本地账号池,没有 edge 拓扑,scan-edge-health.sh
// 对它们无意义——处置就是直接为该平台补充账号(线上 2026-06-17 反馈"我就开始配号"),
// 或核对席位是否被打满。给错平台的 CTA 会在事故里把运维带偏,故按平台分流。
func poolExhaustedAdvice(platform string) string {
	if strings.TrimSpace(platform) == PlatformAnthropic {
		return "先跑 ops/observability/scan-edge-health.sh 看各 edge 自身健康;若 reason 是 anthropic_upstream_error 且各账号几乎同秒进入冷却,优先怀疑单请求确定性 429(如长上下文 usage-credits 策略拒绝)被 failover 扇出,而非账号/edge 真实故障。"
	}
	return "该平台号池已空,需立即为该平台补充可调度账号;若现有账号均健康却仍不可调度,多为并发/会话席位被打满或集中冷却,核对各账号 concurrency/max_sessions 余量与冷却状态。"
}

// poolRecoveryLoop 周期性把「待恢复」台账里的平台拉回——查到可调度账号数 > 0 即发绿卡闭环。
func (n *TKAccountIncidentNotifier) poolRecoveryLoop() {
	if n == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("pool_recovery_loop_panic_recovered", "panic", r)
		}
	}()
	for {
		timer := time.NewTimer(poolRecoveryCheckInterval)
		select {
		case <-n.stopCh:
			timer.Stop()
			return
		case <-timer.C:
			n.checkPoolRecovery()
		}
	}
}

// checkPoolRecovery 遍历当前处于空池告警态的平台,逐个查可调度账号数;> 0 则发「池已恢复」
// 绿卡并把该平台从台账摘除。摘除前在锁内复核仍在台账(防与并发的再次空池/重复恢复竞态),
// 保证每个平台一次恢复事件只发一张绿卡。
func (n *TKAccountIncidentNotifier) checkPoolRecovery() {
	if n == nil {
		return
	}
	n.mu.Lock()
	counter := n.poolSchedulableCounter
	if counter == nil || len(n.poolExhaustSentAt) == 0 {
		n.mu.Unlock()
		return
	}
	pending := make(map[string]time.Time, len(n.poolExhaustSentAt))
	for platform, sentAt := range n.poolExhaustSentAt {
		pending[platform] = sentAt
	}
	n.mu.Unlock()

	for platform, firstAlert := range pending {
		ctx, cancel := context.WithTimeout(context.Background(), poolRecoveryCheckTimeout)
		count, err := counter(ctx, platform)
		cancel()
		if err != nil {
			slog.Warn("pool_recovery_check_query_failed", "platform", platform, "error", err)
			continue
		}
		if count <= 0 {
			continue
		}
		// 复核仍待恢复后再摘除+发卡。若期间已被其它路径摘除(如重复触发),跳过不重发。
		n.mu.Lock()
		if _, still := n.poolExhaustSentAt[platform]; !still {
			n.mu.Unlock()
			continue
		}
		delete(n.poolExhaustSentAt, platform)
		n.mu.Unlock()

		now := n.currentTime()
		title := fmt.Sprintf("TokenKey 平台池已恢复 [%s]", n.siteID)
		body := buildPoolRecoveredText(n.siteID, platform, count, firstAlert, now)
		n.send(title, "green", body, fmt.Sprintf("platform=%s recovered count=%d", platform, count))
	}
}

func buildPoolRecoveredText(site, platform string, schedulable int, firstAlert, now time.Time) string {
	durText := "未知"
	if !firstAlert.IsZero() {
		d := now.Sub(firstAlert)
		if d < 0 {
			d = 0
		}
		durText = formatPoolOutageDuration(d)
	}
	return fmt.Sprintf("**节点**：%s\n**平台**：%s\n**事件**：该平台已重新有可调度账号,流量恢复正常\n**当前可调度账号数**：%d\n**空池持续时长**：%s\n**时间**：%s\n\n**说明**：无需继续补号。若为冷却到期自愈,后续仍可能复发,关注是否反复触发。",
		escapeFeishuText(site),
		escapeFeishuText(strings.TrimSpace(platform)),
		schedulable,
		escapeFeishuText(durText),
		escapeFeishuText(formatAlertTime(now)),
	)
}

// formatPoolOutageDuration 把空池持续时长渲染成「X分Y秒 / X分 / Y秒」的中文短串。
func formatPoolOutageDuration(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	if total < 60 {
		return fmt.Sprintf("%d秒", total)
	}
	m := total / 60
	s := total % 60
	if s == 0 {
		return fmt.Sprintf("%d分", m)
	}
	return fmt.Sprintf("%d分%d秒", m, s)
}
