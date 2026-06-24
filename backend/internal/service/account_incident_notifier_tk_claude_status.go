package service

// TK: status.claude.com 上游 Claude API 故障 → 即时 P0 飞书卡片（及恢复绿卡）。
//
// 与 ratelimit 里「incident 期间不罚账号」的旧逻辑解耦：探测到 Anthropic 侧事件时只
// 通知运营自行判断，账号级 401/429/403 等仍走常规 SetError / 冷却阶梯。

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const claudeAPIIncidentAlertDedupeWindow = 10 * time.Minute

// ClaudeAPIStatusNotifier 是 status.claude.com 轮询器的最小通知面。
type ClaudeAPIStatusNotifier interface {
	NotifyClaudeAPIIncidentStarted(status string)
	NotifyClaudeAPIIncidentResolved(status string)
}

// NotifyClaudeAPIIncidentStarted 在 Claude API 从 operational 转为非 operational 时发 P0 红卡。
func (n *TKAccountIncidentNotifier) NotifyClaudeAPIIncidentStarted(status string) {
	if n == nil || n.isEdgeSite() {
		return
	}
	now := n.currentTime()
	n.mu.Lock()
	if !n.claudeIncidentSentAt.IsZero() && now.Sub(n.claudeIncidentSentAt) < claudeAPIIncidentAlertDedupeWindow {
		n.mu.Unlock()
		return
	}
	n.claudeIncidentSentAt = now
	n.mu.Unlock()

	title := fmt.Sprintf("TokenKey Claude API 上游故障 [%s]", n.siteID)
	body := buildClaudeAPIIncidentStartedText(n.siteID, status, now)
	n.send(title, "red", body, "claude_api_incident_started")
}

// NotifyClaudeAPIIncidentResolved 在 Claude API 恢复 operational 且此前发过故障卡时发绿卡闭环。
func (n *TKAccountIncidentNotifier) NotifyClaudeAPIIncidentResolved(status string) {
	if n == nil || n.isEdgeSite() {
		return
	}
	n.mu.Lock()
	if n.claudeIncidentSentAt.IsZero() {
		n.mu.Unlock()
		return
	}
	n.claudeIncidentSentAt = time.Time{}
	n.mu.Unlock()

	title := fmt.Sprintf("TokenKey Claude API 上游已恢复 [%s]", n.siteID)
	body := buildClaudeAPIIncidentResolvedText(n.siteID, status, n.currentTime())
	n.send(title, "green", body, "claude_api_incident_resolved")
}

func buildClaudeAPIIncidentStartedText(site, status string, now time.Time) string {
	return fmt.Sprintf("**节点**：%s\n**事件**：Anthropic Claude API 上游正在报告故障（status.claude.com: %s）\n**时间**：%s\n\n**建议**：这是 Anthropic 侧 provider 事件，不是 TokenKey 账号池问题。关注 %s 与 Anthropic 官方动态；若出现 anthropic 账号 401/403 等认证类错误，系统会照常 SetError 永久摘号——请运营判断是 refresh token、重新 OAuth 还是上游误报后再处置。",
		escapeFeishuText(site),
		escapeFeishuText(strings.TrimSpace(status)),
		escapeFeishuText(formatAlertTime(now)),
		escapeFeishuText(claudeStatusPageURL),
	)
}

func buildClaudeAPIIncidentResolvedText(site, status string, now time.Time) string {
	return fmt.Sprintf("**节点**：%s\n**事件**：Anthropic Claude API 已恢复 operational（当前状态: %s）\n**时间**：%s\n\n**说明**：此前 Claude API 上游故障 P0 已闭环；若仍有单账号 auth 错误，按账号永久失效卡片单独处理。",
		escapeFeishuText(site),
		escapeFeishuText(strings.TrimSpace(status)),
		escapeFeishuText(formatAlertTime(now)),
	)
}

var (
	claudeAPIStatusNotifierMu sync.RWMutex
	claudeAPIStatusNotifier   ClaudeAPIStatusNotifier
)

// SetClaudeAPIStatusNotifier registers the Feishu notifier for status.claude.com
// transitions. Nil-safe; call before StartClaudeStatusPoller.
func SetClaudeAPIStatusNotifier(n ClaudeAPIStatusNotifier) {
	claudeAPIStatusNotifierMu.Lock()
	claudeAPIStatusNotifier = n
	claudeAPIStatusNotifierMu.Unlock()
}

func getClaudeAPIStatusNotifier() ClaudeAPIStatusNotifier {
	claudeAPIStatusNotifierMu.RLock()
	defer claudeAPIStatusNotifierMu.RUnlock()
	return claudeAPIStatusNotifier
}
