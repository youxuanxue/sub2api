package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// TkClientFacingUnsupportedMessage is the ONLY client-visible body message when
// an upstream (or internal) error would otherwise expose supply-side product /
// plan branding — Agent Plan, Token Plan, vendor docs URLs that name those
// products, etc. Wire bodies must never tell callers which subscription SKU
// fulfilled the request.
//
// Internal diagnostics keep the original text via SetOpsUpstreamError /
// MarkOpsStreamError (first-wins) and structured logs.
const TkClientFacingUnsupportedMessage = "The requested model is not supported."

// Historical / live needles that reveal TokenKey supply products. Match is
// case-insensitive substring. Keep this list SSOT — do not hand-copy into
// handlers or remediatescripts.
var tkUpstreamProductLeakNeedles = []string{
	"agent plan",
	"agent-plan",
	"token plan",
	"token-plan",
	"tokenplan",
	"ali token plan",
	"ali-token-plan",
	"volcengine.com/docs",
}

// TkLooksLikeUpstreamProductLeakMessage reports whether msg would expose
// upstream plan/product branding to the client.
func TkLooksLikeUpstreamProductLeakMessage(msg string) bool {
	low := strings.ToLower(strings.TrimSpace(msg))
	if low == "" {
		return false
	}
	for _, needle := range tkUpstreamProductLeakNeedles {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

// TkIsClientFacingUnsupportedMessage reports whether msg is the current public copy.
func TkIsClientFacingUnsupportedMessage(msg string) bool {
	return strings.EqualFold(strings.TrimSpace(msg), TkClientFacingUnsupportedMessage)
}

// TkSanitizeUpstreamProductWireMessage rewrites plan/product-branded phrases to
// the public unsupported SSOT. Returns (publicMessage, sanitized).
// Call at response choke points so upstream passthrough / ClientMessage /
// NewAPIRelayError / our own fmt.Errorf cannot ship supply SKU names on the wire.
func TkSanitizeUpstreamProductWireMessage(message string) (string, bool) {
	if TkIsClientFacingUnsupportedMessage(message) {
		return TkClientFacingUnsupportedMessage, false
	}
	if TkLooksLikeUpstreamProductLeakMessage(message) {
		return TkClientFacingUnsupportedMessage, true
	}
	return message, false
}

// TkAnnotateUpstreamProductOps records INTERNAL detail for ops before a handler
// writes TkClientFacingUnsupportedMessage. MarkOpsStreamError is first-wins.
func TkAnnotateUpstreamProductOps(c *gin.Context, errType string, status int, internalDetail string) {
	if c == nil {
		return
	}
	detail := strings.TrimSpace(internalDetail)
	if detail == "" {
		detail = "upstream_product_capability"
	}
	if strings.TrimSpace(errType) == "" {
		errType = "invalid_request_error"
	}
	MarkOpsStreamError(c, errType, detail, status)
}
