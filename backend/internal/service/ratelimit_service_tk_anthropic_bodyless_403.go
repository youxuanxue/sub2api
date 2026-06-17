package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/tidwall/gjson"
)

// TK：持续「空 body / 非结构化」Anthropic 403 的终局升级。
//
// 背景（handle403 gap, 2026-06-16 edge us6 后续）：#810 的
// tkTryDisableAnthropicOrgBan403 在**结构化 body** 命中 org-ban 短语时首次即永久禁用。
// 但 org 封禁也会以**空 body**（或 HTML / Cloudflare 错误页 / 非结构化 JSON）形态返回
// 403——id=1 历史上即如此（failover_loop.go 注释里的 “10 次 403 全部 ResponseBody 空”）。
// 空 body 无短语可匹配，于是逃过 #810，落回 handleAnthropicUpstreamError 的 count-based
// 3/3 阶梯（30s/2m/10m，自动恢复），永久 flap——正是 #810 本要消灭的失败模式。
//
// 本文件补这个缺口：用一个**独立、仅空 body 403** 的窗口计数器累计；在一个窗口内累计到
// 阈值即判定该账号被上游持续拒绝（org-ban 或基础设施层 WAF/指纹失效，二者都需 ops 介入、
// 都必须停止打上游），永久禁用（handleAuthError → SetError，停调度 + 停后台刷新）+ 告警。
//
// 设计要点：
//   - **仅空 body / 非结构化**（tkIsUnstructuredAnthropicErrorBody）。model-level 403
//     （“you do not have access to this model”）带结构化 body，永不计入——杜绝 #810 注释
//     里警告的「一个坏 model 请求误禁健康账号」。
//   - **独立计数器**（IncrementAnthropicBodyless403Count，独立 Redis key），不与 429/5xx
//     混——避免误把限流/过载账号永久禁用。
//   - **尊重 IsClaudeAPIIncident()**：provider 级事件期间不批量禁用全池（与
//     handleAnthropicUpstreamError 的 skipCooldown 同款封套）。
//   - **fail-open**：计数器未注入 / 出错 / 未达阈值 → 返回 false，落回既有阶梯，行为不变。

const (
	// anthropic403BodylessDisableThresholdDefault：一个窗口内累计多少次空 body 403 即判
	// 持续拒绝并永久禁用。永久禁用不可自愈（需 ops），故偏保守——10 次足以在繁忙 edge 上
	// ~一两个冷却周期内坐实持续 flap，又远高于单个请求的偶发瞬时 403。
	anthropic403BodylessDisableThresholdDefault int64 = 10
	// anthropic403BodylessWindowMinutesDefault：累计窗口。30min 覆盖 3/3 阶梯封顶 10min
	// 冷却的 ~两到三个 re-admit 周期，使「冷却→回池→仍空 body 403」的持续 flap 能累积到
	// 阈值，而跨窗口的零星瞬时 403 会随窗口过期清零。
	anthropic403BodylessWindowMinutesDefault int = 30
)

// tkIsUnstructuredAnthropicErrorBody 报告上游错误 body 是否为「非结构化」——空 body、
// 非法 JSON（HTML / Cloudflare 错误页 / 纯文本）、或合法 JSON 但既无 error 对象也无
// 非空 code+message。它是 handler 层 looksLikeStructuredErrorJSON（failover_loop.go）的
// 反面，复制进 service 包以避免 handler→service 循环导入。
func tkIsUnstructuredAnthropicErrorBody(body []byte) bool {
	if len(body) == 0 {
		return true
	}
	if !json.Valid(body) {
		return true
	}
	if gjson.GetBytes(body, "error").IsObject() {
		return false
	}
	code := strings.TrimSpace(gjson.GetBytes(body, "code").String())
	msg := strings.TrimSpace(gjson.GetBytes(body, "message").String())
	return code == "" || msg == ""
}

// tkTryEscalatePersistentBodyless403 在本次 Anthropic 403 为空 body / 非结构化时累计计数，
// 达阈值即永久禁用 + 告警并返回 true（调用方据此 return，跳过既有 3/3 冷却阶梯）。否则
// 返回 false，落回既有阶梯，行为不变。调用点在 handle403 的 Anthropic 分支、
// tkTryDisableAnthropicOrgBan403（结构化短语）与 TLS 指纹检查之后、阶梯回退之前。
func (s *RateLimitService) tkTryEscalatePersistentBodyless403(ctx context.Context, account *Account, upstreamMsg string, responseBody []byte) bool {
	if s == nil || account == nil || s.anthropicUpstreamErrorCounterCache == nil {
		return false
	}
	// 只处理空 body / 非结构化 403。结构化 body（含 model-level 拒绝、以及已被 #810 捕获的
	// org-ban 短语）一律不计入、不升级。
	if !tkIsUnstructuredAnthropicErrorBody(responseBody) {
		return false
	}
	// Provider 级事件期间（status.claude.com 报 Claude API 非运营）不升级，避免把全池因
	// 上游短时故障批量永久禁用——与 handleAnthropicUpstreamError 的 skipCooldown 同款。
	if IsClaudeAPIIncident() {
		return false
	}

	count, err := s.anthropicUpstreamErrorCounterCache.IncrementAnthropicBodyless403Count(
		ctx, account.ID, anthropic403BodylessWindowMinutesDefault)
	if err != nil {
		slog.Warn("anthropic_bodyless_403_counter_increment_failed", "account_id", account.ID, "error", err)
		return false // fail-open
	}
	if count < anthropic403BodylessDisableThresholdDefault {
		return false
	}

	msg := buildForbiddenErrorMessage(
		"Persistent bodyless/unstructured Anthropic 403 (likely org ban or infra/WAF block):",
		upstreamMsg,
		responseBody,
		"upstream returned repeated 403 with no structured error body",
	)
	// greppable marker（§8.5 排障约定）——区别于 #810 的结构化 org-ban（替换账号）与
	// transient anthropic_upstream_error 阶梯，提示 ops 调查（org-ban vs 指纹/WAF）后再处置。
	slog.Warn("anthropic_persistent_bodyless_403_disable",
		"account_id", account.ID,
		"platform", account.Platform,
		"count", count,
		"threshold", anthropic403BodylessDisableThresholdDefault,
		"window_minutes", anthropic403BodylessWindowMinutesDefault,
		"action", "ops_should_investigate_org_ban_or_tls_fingerprint_then_replace_or_recapture",
		"upstream_msg", upstreamMsg,
	)
	s.handleAuthError(ctx, account, msg)
	return true
}
