package service

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// OpenAICapacityShedClientRetryAfterSeconds exposes the client retry advice the
// capacity-shed rewrite already attaches (sanitizeOpenAICapacityShedErrorCodeForClient)
// so the Responses WebSocket terminal error frame uses the same backoff.
const OpenAICapacityShedClientRetryAfterSeconds = openAICapacityShedClientRetryAfterSeconds

// OpenAIUsageLimitReachedReset reports whether an upstream error body is an
// account usage limit (error.type/code == usage_limit_reached) and, when the
// body carries one, its reset instant. Plain rate limits (rate_limit_exceeded)
// are not usage limits: Codex treats usage_limit_reached as terminal, so it is
// only claimed when the upstream said so.
func OpenAIUsageLimitReachedReset(body []byte, now time.Time) (bool, time.Time) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false, time.Time{}
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()))
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	if errType != "usage_limit_reached" && code != "usage_limit_reached" {
		return false, time.Time{}
	}
	if errType != "usage_limit_reached" {
		// parseOpenAIRateLimitResetTime keys on error.type; normalize code-only envelopes.
		return true, openAIUsageLimitResetFromFields(body, now)
	}
	if resetAt := parseOpenAIRateLimitResetTime(body); resetAt != nil && *resetAt > now.Unix() {
		return true, time.Unix(*resetAt, 0)
	}
	return true, time.Time{}
}

func openAIUsageLimitResetFromFields(body []byte, now time.Time) time.Time {
	if resetsAt := gjson.GetBytes(body, "error.resets_at").Int(); resetsAt > now.Unix() {
		return time.Unix(resetsAt, 0)
	}
	if seconds := gjson.GetBytes(body, "error.resets_in_seconds").Int(); seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	return time.Time{}
}
