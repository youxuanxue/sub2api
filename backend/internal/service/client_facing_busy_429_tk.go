package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// TkClientFacingBusy429Message is the ONLY client-visible HTTP 429 body message
// for gateway capacity (empty / unschedulable account pool) and upstream
// rate-limit exhaustion. Wire bodies must never expose pool/account wording.
//
// Internal diagnostics keep the detailed phrases via TkAnnotateBusy429Ops,
// SetOpsUpstreamError, MarkOpsStreamError (first-wins), and structured logs.
//
// error.type stays site-specific so nested-stub penalty skip can still tell
// empty-pool (api_error) from upstream-limit (rate_limit_error) after the
// public message is unified:
//
//	empty pool / capacity     → type=api_error,        message=this constant
//	upstream / mapped 429     → type=rate_limit_error, message=this constant
const TkClientFacingBusy429Message = "The service is temporarily busy. Please try again later."

// Historical client-visible phrases. Detectors and ops matchers still recognize
// them for in-flight / nested-gateway bodies minted before this SSOT landed.
const (
	tkLegacyNoAvailableAccountsNeedle = "no available accounts"
	tkLegacyUpstreamRateLimitNeedle   = "upstream rate limit exceeded"
	tkLegacyAccountsRateLimitedNeedle = "all available accounts are currently rate-limited"
	tkLegacyUniversalCapacityNeedle   = "no available accounts for this request"
	tkLegacyKiroQuotaNeedle           = "kiro upstream quota exhausted"
	tkLegacyCompactAccountsNeedle     = "no available openai accounts support"
)

// TkAnnotateBusy429Ops records INTERNAL detail for ops before a handler writes
// TkClientFacingBusy429Message to the client. MarkOpsStreamError is first-wins,
// so this MUST run before handleStreamingAwareError on streaming paths.
func TkAnnotateBusy429Ops(c *gin.Context, errType, internalDetail string) {
	if c == nil {
		return
	}
	detail := strings.TrimSpace(internalDetail)
	if detail == "" {
		detail = "capacity_or_upstream_limit"
	}
	if strings.TrimSpace(errType) == "" {
		errType = "api_error"
	}
	SetOpsUpstreamError(c, http.StatusTooManyRequests, detail, "")
	MarkOpsStreamError(c, errType, detail, http.StatusTooManyRequests)
}

// TkIsClientFacingBusy429Message reports whether msg is the current public copy.
func TkIsClientFacingBusy429Message(msg string) bool {
	return strings.EqualFold(strings.TrimSpace(msg), TkClientFacingBusy429Message)
}

// TkLooksLikeLegacyCapacityClientMessage reports historical empty-pool /
// capacity phrases (case-insensitive substring). Used by ops attribution and
// nested-stub penalty skip alongside the current public copy.
func TkLooksLikeLegacyCapacityClientMessage(msg string) bool {
	low := strings.ToLower(strings.TrimSpace(msg))
	if low == "" {
		return false
	}
	switch {
	case strings.Contains(low, tkLegacyNoAvailableAccountsNeedle),
		strings.Contains(low, tkLegacyAccountsRateLimitedNeedle),
		strings.Contains(low, tkLegacyUniversalCapacityNeedle),
		strings.Contains(low, tkLegacyCompactAccountsNeedle):
		return true
	default:
		return false
	}
}

// TkLooksLikeLegacyUpstreamLimitClientMessage reports historical upstream-limit
// rewrite phrases (not raw provider quota prose).
func TkLooksLikeLegacyUpstreamLimitClientMessage(msg string) bool {
	low := strings.ToLower(strings.TrimSpace(msg))
	if low == "" {
		return false
	}
	return strings.Contains(low, tkLegacyUpstreamRateLimitNeedle) ||
		strings.Contains(low, tkLegacyKiroQuotaNeedle)
}

// TkBodyLooksLikeGatewayCapacityBusy429 is true when a nested TokenKey (or
// historical) empty-pool / capacity envelope is present. Upstream-limit busy
// 429s use rate_limit_error and must NOT match — those still cool the stub.
func TkBodyLooksLikeGatewayCapacityBusy429(upstreamMsg string, responseBody []byte) bool {
	if TkLooksLikeLegacyCapacityClientMessage(upstreamMsg) {
		return true
	}
	bodyMsg := strings.TrimSpace(gjson.GetBytes(responseBody, "error.message").String())
	if bodyMsg == "" {
		bodyMsg = strings.TrimSpace(string(responseBody))
	}
	if TkLooksLikeLegacyCapacityClientMessage(bodyMsg) {
		return true
	}
	// Current public copy: treat as capacity when error.type is api_error
	// (empty-pool path) OR stable machine code no_available_accounts (universal
	// OpenAI shape uses rate_limit_error + that code). Plain rate_limit_error +
	// busy message without the code is upstream-limit and must NOT match.
	if !TkIsClientFacingBusy429Message(upstreamMsg) && !TkIsClientFacingBusy429Message(bodyMsg) {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.code").String()))
	if code == "no_available_accounts" {
		return true
	}
	errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "error.type").String()))
	if errType == "" {
		errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(responseBody, "type").String()))
	}
	return errType == "api_error" || errType == ""
}

// TkMapUpstreamStatusToClient429 is the shared 429 branch for mapUpstreamError
// helpers. Returns (status, errType, publicMessage).
func TkMapUpstreamStatusToClient429() (int, string, string) {
	return http.StatusTooManyRequests, "rate_limit_error", TkClientFacingBusy429Message
}

// TkSanitizeBusy429WireMessage rewrites legacy capacity / upstream-limit phrases
// on HTTP 429 to the public SSOT copy. Returns (publicMessage, sanitized).
// Call at response choke points so ClientMessage / passthrough leaks cannot
// ship pool/account wording to the wire.
func TkSanitizeBusy429WireMessage(status int, message string) (string, bool) {
	if status != http.StatusTooManyRequests {
		return message, false
	}
	if TkIsClientFacingBusy429Message(message) {
		return TkClientFacingBusy429Message, false
	}
	if TkLooksLikeLegacyCapacityClientMessage(message) || TkLooksLikeLegacyUpstreamLimitClientMessage(message) {
		return TkClientFacingBusy429Message, true
	}
	return message, false
}
