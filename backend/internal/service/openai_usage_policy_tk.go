package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// opsUsagePolicyKey carries an OpenAI usage-policy hard-block mark on gin context.
// Parallel to opsCyberPolicyKey: request-scoped, first-write wins, used to write
// the shared cyber-session block table and ops_error_logs after forward returns.
const opsUsagePolicyKey = "ops_usage_policy"

// UsagePolicyMark records upstream evidence for an OpenAI usage-policy rejection
// (Invalid prompt / violating usage policy). Distinct from cyber_policy.
type UsagePolicyMark struct {
	Code           string // fixed "usage_policy"
	Message        string
	Body           string
	UpstreamStatus int
	UpstreamInTok  int
	UpstreamOutTok int
}

// MarkOpsUsagePolicy records a usage-policy mark; first write wins per request/turn.
func MarkOpsUsagePolicy(c *gin.Context, mark UsagePolicyMark) {
	if c == nil {
		return
	}
	if GetOpsUsagePolicy(c) != nil {
		return
	}
	mark.Code = "usage_policy"
	mark.Message = strings.TrimSpace(mark.Message)
	mark.Body = strings.TrimSpace(mark.Body)
	c.Set(opsUsagePolicyKey, &mark)
}

// GetOpsUsagePolicy returns the usage-policy mark, or nil.
func GetOpsUsagePolicy(c *gin.Context) *UsagePolicyMark {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(opsUsagePolicyKey); ok {
		if m, ok := v.(*UsagePolicyMark); ok && m != nil {
			return m
		}
	}
	return nil
}

// ClearOpsUsagePolicy clears the usage-policy mark (WS turn lifecycle).
func ClearOpsUsagePolicy(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(opsUsagePolicyKey, (*UsagePolicyMark)(nil))
}

// detectOpenAIUsagePolicy identifies OpenAI "Invalid prompt … usage policy"
// rejections. Returns (hit, message). Deliberately tight: requires "usage policy"
// / "violating our usage" text, or invalid_prompt + violat — not bare "policy".
// Only structured error fields / upstreamMsg are scanned — never the raw body —
// so echoed user prompts cannot false-trigger session isolation.
func detectOpenAIUsagePolicy(upstreamMsg string, payload []byte) (bool, string) {
	msg := strings.TrimSpace(upstreamMsg)
	bodyMsg := strings.TrimSpace(gjson.GetBytes(payload, "error.message").String())
	if bodyMsg == "" {
		bodyMsg = strings.TrimSpace(gjson.GetBytes(payload, "response.error.message").String())
	}
	if msg == "" {
		msg = bodyMsg
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
	if code == "" {
		code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
	}
	// Match only structured fields + caller-supplied upstreamMsg. Never scan the
	// raw body (echoed prompts must not false-trigger session isolation).
	lower := strings.ToLower(strings.TrimSpace(strings.Join([]string{msg, bodyMsg, code, errType}, " ")))
	if lower == "" {
		return false, ""
	}
	if strings.Contains(lower, "usage policy") ||
		strings.Contains(lower, "violating our usage") ||
		strings.Contains(lower, "violate our usage") {
		if strings.TrimSpace(msg) == "" {
			return true, bodyMsg
		}
		return true, strings.TrimSpace(msg)
	}
	if (code == "invalid_prompt" || errType == "invalid_prompt") && strings.Contains(lower, "violat") {
		if strings.TrimSpace(msg) == "" {
			return true, bodyMsg
		}
		return true, strings.TrimSpace(msg)
	}
	return false, ""
}

// isOpenAISafetySessionBlockFault is true for cyber_policy or usage_policy —
// both must SharedFault (no account failover / same-prompt auto-retry) and may
// write the session block table.
func isOpenAISafetySessionBlockFault(upstreamMsg string, payload []byte) bool {
	if hit, _, _ := detectOpenAICyberPolicy(payload); hit {
		return true
	}
	hit, _ := detectOpenAIUsagePolicy(upstreamMsg, payload)
	return hit
}

func markOpenAIUsagePolicyEvent(c *gin.Context, payload []byte, upstreamStatus int, usage *OpenAIUsage) bool {
	hit, message := detectOpenAIUsagePolicy("", payload)
	if !hit {
		return false
	}
	mark := UsagePolicyMark{
		Code:           "usage_policy",
		Message:        message,
		Body:           truncateString(string(payload), 4096),
		UpstreamStatus: upstreamStatus,
	}
	if usage != nil {
		mark.UpstreamInTok = usage.InputTokens
		mark.UpstreamOutTok = usage.OutputTokens
	}
	MarkOpsUsagePolicy(c, mark)
	return true
}

// markOpenAISafetyPolicyEvent marks cyber_policy (preferred) or usage_policy.
// Returns which kind was marked ("" if neither).
func markOpenAISafetyPolicyEvent(c *gin.Context, payload []byte, upstreamStatus int, usage *OpenAIUsage) string {
	if markOpenAICyberPolicyEvent(c, payload, upstreamStatus, usage) {
		return "cyber_policy"
	}
	if markOpenAIUsagePolicyEvent(c, payload, upstreamStatus, usage) {
		return "usage_policy"
	}
	return ""
}
