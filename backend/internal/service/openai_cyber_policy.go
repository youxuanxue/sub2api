package service

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// opsCyberPolicyKey 在 gin context 中携带 cyber_policy 命中标记。
// 由 gateway 服务层在检测到上游 error.code=="cyber_policy" 时设置，
// handler 在 Forward 返回后读取以触发风控记录、邮件与 tokens=0 用量行。
const opsCyberPolicyKey = "ops_cyber_policy"

// OpenAI cyber / session-isolation error.code SSOT.
// Upstream provider uses OpenAICyberPolicyErrorCode; TokenKey local session-block
// responses use OpenAICyberSessionBlockedErrorCode. Both must share the same
// detect → SharedFault / no-failover → mark → session-block write path so a
// mirror/edge 403 cannot rotate the pool (prod→edge fanout gap).
const (
	OpenAICyberPolicyErrorCode         = "cyber_policy"
	OpenAICyberSessionBlockedErrorCode = "session_blocked_by_cyber_policy"
)

// openAIErrorCodeIsCyberSafetyBlock is the single structured-code gate for
// cyber safety session isolation. All failover adapters and mark helpers must
// go through detectOpenAICyberPolicy (which calls this), not re-match codes.
func openAIErrorCodeIsCyberSafetyBlock(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case OpenAICyberPolicyErrorCode, OpenAICyberSessionBlockedErrorCode:
		return true
	default:
		return false
	}
}

// errOpenAICyberPolicyForwarded 表示 cyber_policy 已按当前端点格式透传给客户端
// （error 已写出/下发）。compat 路径 ForwardAsChatCompletions / ForwardAsAnthropic 出口
// 据此丢弃 result 并返回该哨兵，使 handler 落入 tokens=0 免费用量行（对齐 /v1/responses），
// 既不计费、也不 failover、不重复写响应。
var errOpenAICyberPolicyForwarded = errors.New("openai cyber_policy forwarded to client")

// CyberPolicyMark 记录一次 cyber 安全会话隔离命中证据。
type CyberPolicyMark struct {
	// Code is OpenAICyberPolicyErrorCode (upstream) or
	// OpenAICyberSessionBlockedErrorCode (TokenKey local / mirror echo).
	// Local echo must not feed RecordCyberPolicyEvent ban counting.
	Code           string
	Message        string // 上游 / 本地 error.message
	Body           string // 上游 response.failed / 400 原始 body（已截断；未脱敏，ops_error 落库由 sanitizeErrorBodyForStorage、风控日志由 redactContentModerationSecrets 统一脱敏）
	UpstreamStatus int    // 上游 HTTP 状态（流式=200，非流式=400；本地 echo 多为 403）
	UpstreamInTok  int    // 上游已报 input tokens（如有）
	UpstreamOutTok int    // 上游已报 output tokens（如有）
}

// canonicalizeCyberPolicyMarkCode keeps local session-block identity distinct
// from upstream cyber_policy while accepting case variants of either SSOT code.
func canonicalizeCyberPolicyMarkCode(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case OpenAICyberSessionBlockedErrorCode:
		return OpenAICyberSessionBlockedErrorCode
	default:
		return OpenAICyberPolicyErrorCode
	}
}

// IsOpenAICyberSessionBlockedMark reports a TokenKey local / mirror-echo
// session-block mark (not a fresh upstream cyber_policy hit).
func IsOpenAICyberSessionBlockedMark(mark *CyberPolicyMark) bool {
	return mark != nil && mark.Code == OpenAICyberSessionBlockedErrorCode
}

// MarkOpsCyberPolicy 记录 cyber 标记；首个写入生效，后续忽略（同一 turn 只记一次）。
// WS 多轮场景由 handler 在每个 turn 结束后调用 ClearOpsCyberPolicy 重置。
func MarkOpsCyberPolicy(c *gin.Context, mark CyberPolicyMark) {
	if c == nil {
		return
	}
	if GetOpsCyberPolicy(c) != nil {
		return
	}
	mark.Code = canonicalizeCyberPolicyMarkCode(mark.Code)
	mark.Message = strings.TrimSpace(mark.Message)
	mark.Body = strings.TrimSpace(mark.Body)
	c.Set(opsCyberPolicyKey, &mark)
}

// GetOpsCyberPolicy 返回 cyber 标记，未命中（或已被 Clear）返回 nil。
func GetOpsCyberPolicy(c *gin.Context) *CyberPolicyMark {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(opsCyberPolicyKey); ok {
		if m, ok := v.(*CyberPolicyMark); ok && m != nil {
			return m
		}
	}
	return nil
}

// ClearOpsCyberPolicy 清除 cyber 标记（typed-nil 覆盖；gin context 无并发安全的
// 删除原语，Set 走内部锁，与异步 GetOpsCyberPolicy 不构成 data race）。
// 仅 WS 多轮路径在 turn 收尾调用；HTTP 单请求路径不调用（context 随请求销毁，
// 且中间件 shouldSkipOpsErrorLogForCyber 依赖标记防双写）。
// WS 路径 clear 发生在中间件收尾之前，连接响应状态为 101，不触发中间件 status>=400
// 落库分支，故无双写/漏写。
func ClearOpsCyberPolicy(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(opsCyberPolicyKey, (*CyberPolicyMark)(nil))
}

// detectOpenAICyberPolicy 识别 cyber 安全会话隔离类 structured error.code：
// 上游 cyber_policy（对齐 codex api_bridge.rs:145 / sse/responses.rs:529）以及
// TokenKey 本地 session_blocked_by_cyber_policy（edge/mirror 回流）。
// SharedFault / no-failover 共用 openAIErrorCodeIsCyberSafetyBlock；返回的 code
// 保留上游 vs 本地身份，供 mark / ops / 风控计数分支（本地 echo 不进封号）。
func detectOpenAICyberPolicy(payload []byte) (bool, string, string) {
	code := gjson.GetBytes(payload, "error.code").String()
	if code == "" {
		code = gjson.GetBytes(payload, "response.error.code").String()
	}
	if !openAIErrorCodeIsCyberSafetyBlock(code) {
		return false, "", ""
	}
	msg := gjson.GetBytes(payload, "error.message").String()
	if msg == "" {
		msg = gjson.GetBytes(payload, "response.error.message").String()
	}
	return true, canonicalizeCyberPolicyMarkCode(code), strings.TrimSpace(msg)
}

func markOpenAICyberPolicyEvent(c *gin.Context, payload []byte, upstreamStatus int, usage *OpenAIUsage) bool {
	hit, code, message := detectOpenAICyberPolicy(payload)
	if !hit {
		return false
	}
	mark := CyberPolicyMark{
		Code:           code,
		Message:        message,
		Body:           truncateString(string(payload), 4096),
		UpstreamStatus: upstreamStatus,
	}
	if usage != nil {
		mark.UpstreamInTok = usage.InputTokens
		mark.UpstreamOutTok = usage.OutputTokens
	}
	MarkOpsCyberPolicy(c, mark)
	return true
}
