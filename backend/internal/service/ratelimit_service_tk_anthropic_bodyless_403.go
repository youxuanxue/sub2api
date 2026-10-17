package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	// anthropic403BodylessDisableThresholdDefault：达多少个**独立冷却 EPISODE**(经
	// debounce 折叠并发突发后)的空 body 403 即判持续拒绝并永久禁用。永久禁用不可自愈
	// (需 ops),故按 EPISODE 计而非按请求计——5 个独立 episode(冷却→回池→仍空 body 403)
	// 是不可能由单次瞬时基础设施抖动产生的(那只是 1 个 episode),又能在持续 flap 时于
	// ~半小时内坐实。
	anthropic403BodylessDisableThresholdDefault int64 = 5
	// anthropic403BodylessWindowMinutesDefault：episode 累计窗口。60min 覆盖 3/3 阶梯
	// 升到封顶 10min 冷却后的 ~5 个 re-admit episode,跨窗口的零星 episode 随窗口过期清零。
	anthropic403BodylessWindowMinutesDefault int = 60
	// anthropic403BodylessDebounceSecondsDefault：把一个失败 episode 内的并发突发(同时
	// 在途的多个请求各打一次空 body 403)折叠成 1 次计数的去抖窗口。取 15s——小于 3/3
	// 阶梯最短 30s 冷却(故相邻 episode 必被分开计),又远大于亚秒级并发突发(故突发折叠)。
	anthropic403BodylessDebounceSecondsDefault int = 15
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

	count, err := s.anthropicUpstreamErrorCounterCache.IncrementAnthropicBodyless403Count(
		ctx, account.ID, anthropic403BodylessWindowMinutesDefault, anthropic403BodylessDebounceSecondsDefault)
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
