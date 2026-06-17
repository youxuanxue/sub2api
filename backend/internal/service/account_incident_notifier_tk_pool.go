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
