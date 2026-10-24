package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// TkClientFacingRequestTooLargeMessage is the ONLY client-visible body message
// for a request that exceeded an upstream wire-size limit. It deliberately
// states nothing beyond the fact itself: not which supplier enforced the
// limit, not the byte counts, and not that TokenKey had already truncated
// conversation history trying to fit it. Those are supply-side implementation
// details — a caller can only act on "the request is too large".
//
// Mirrors TkClientFacingUnsupportedMessage / TkClientFacingBusy429Message:
// internal diagnostics keep the original text via TkAnnotateRequestTooLargeOps
// and structured logs, so ops loses nothing.
const TkClientFacingRequestTooLargeMessage = "Request too large"

// tkSupplierLeakExtraNeedles are supplier identifiers that are NOT platform
// registry keys: vendor runtimes and brand spellings a caller must never see.
// The registry-derived names below cover the rest.
var tkSupplierLeakExtraNeedles = []string{
	"codewhisperer",
	"dashscope",
	"qianfan",
	"volcengine",
	"moonshot",
	"bailian",
}

// tkPlatformsExemptFromSupplierLeak are registry keys that are NOT supply-side
// branding and may legitimately appear in client copy: the protocol families
// callers address us with, and internal routing labels with no vendor meaning.
var tkPlatformsExemptFromSupplierLeak = map[string]bool{
	PlatformAnthropic: true, // the API shape the caller speaks
	PlatformOpenAI:    true, // ditto
	PlatformGemini:    true, // ditto
	PlatformNewAPI:    true, // internal relay label
	PlatformComposite: true, // internal routing label
	PlatformTypeSafe:  true, // internal label
}

// tkSupplierLeakNeedles is derived from the platform registry rather than
// hand-copied: a new platform must not silently escape this gate. A hand-kept
// list is exactly how "Grok OAuth" stayed on the wire after the Kiro fix.
var tkSupplierLeakNeedles = buildTkSupplierLeakNeedles()

func buildTkSupplierLeakNeedles() []string {
	needles := make([]string, 0, len(tkSupplierLeakExtraNeedles)+16)
	needles = append(needles, tkSupplierLeakExtraNeedles...)
	for _, platform := range []string{
		PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity,
		PlatformNewAPI, PlatformKiro, PlatformGrok, PlatformKimi, PlatformZhipu,
		PlatformDeepseek, PlatformMiniMax, PlatformTypeSafe, PlatformOpenCodeGo,
		PlatformCommandCode, PlatformCline, PlatformComposite,
	} {
		key := strings.ToLower(strings.TrimSpace(platform))
		if key == "" || tkPlatformsExemptFromSupplierLeak[platform] {
			continue
		}
		needles = append(needles, key)
	}
	return needles
}

// TkLooksLikeUpstreamSupplierLeakMessage reports whether msg names the
// fulfilling supplier. Case-insensitive substring match.
func TkLooksLikeUpstreamSupplierLeakMessage(msg string) bool {
	low := strings.ToLower(strings.TrimSpace(msg))
	if low == "" {
		return false
	}
	for _, needle := range tkSupplierLeakNeedles {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

// TkAnnotateRequestTooLargeOps records the INTERNAL oversize detail (byte
// counts, truncation state, supplier) for ops before a handler writes the
// public copy. MarkOpsStreamError is first-wins.
func TkAnnotateRequestTooLargeOps(c *gin.Context, status int, internalDetail string) {
	if c == nil {
		return
	}
	detail := strings.TrimSpace(internalDetail)
	if detail == "" {
		detail = "upstream_request_too_large"
	}
	MarkOpsStreamError(c, "request_too_large", detail, status)
}
