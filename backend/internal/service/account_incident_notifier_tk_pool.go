package service

import (
	"fmt"
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
	return fmt.Sprintf("**节点**：%s\n**平台**：%s\n**事件**：该平台可调度账号数已降为 0,所有流量将快速失败(429)\n**压垮池的最后账号**：%s\n**该账号 reason**：%s\n**该账号冷却至**：%s\n**时间**：%s\n\n**建议**：先跑 ops/observability/scan-edge-health.sh 看各 edge 自身健康;若 reason 是 anthropic_upstream_error 且各账号几乎同秒进入冷却,优先怀疑单请求确定性 429(如长上下文 usage-credits 策略拒绝)被 failover 扇出,而非账号/edge 真实故障。",
		escapeFeishuText(site),
		escapeFeishuText(strings.TrimSpace(platform)),
		escapeFeishuText(triggerLabel),
		escapeFeishuText(strings.TrimSpace(reason)),
		escapeFeishuText(untilText),
		escapeFeishuText(formatAlertTime(now)),
	)
}
