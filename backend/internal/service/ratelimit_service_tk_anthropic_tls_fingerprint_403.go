package service

import (
	"context"
	"log/slog"
	"strings"
)

// tkAnthropicTLSFingerprintDisablePrefix is the greppable SetError prefix for bulk
// recover after canonical TLS profile re-capture. Distinct from org-ban and bodyless.
const tkAnthropicTLSFingerprintDisablePrefix = "TLS fingerprint profile stale (403):"

// tkAnthropicTLSFingerprint403Keywords match Cloudflare / WAF responses that
// reveal the request was rejected on TLS shape rather than OAuth identity.
var tkAnthropicTLSFingerprint403Keywords = []string{
	"ja3",
	"ja4",
	"bot detection",
	"bot management",
	"tls fingerprint",
	"client fingerprint",
}

// tkMatchAnthropicTLSFingerprint403Body reports which TLS/WAF keyword (if any) a
// 403 body matches. Shared with the in-place-retry skip so OAuth 403 retry does
// not hammer upstream with a known-bad canonical profile.
func tkMatchAnthropicTLSFingerprint403Body(upstreamMsg string, responseBody []byte) string {
	haystack := strings.ToLower(strings.TrimSpace(upstreamMsg) + " " + string(responseBody))
	return matchTempUnschedKeyword(haystack, tkAnthropicTLSFingerprint403Keywords)
}

// tkTryDisableAnthropicTLSFingerprint403 permanently disables on TLS/WAF 403 so
// shared canonical profile drift stops exposing OAuth accounts to repeated WAF
// hits. Ops bulk-recovers via error prefix after cc-fingerprint-alignment.
func (s *RateLimitService) tkTryDisableAnthropicTLSFingerprint403(ctx context.Context, account *Account, upstreamMsg string, responseBody []byte) bool {
	if s == nil || account == nil {
		return false
	}
	matched := tkMatchAnthropicTLSFingerprint403Body(upstreamMsg, responseBody)
	if matched == "" {
		return false
	}

	msg := buildForbiddenErrorMessage(
		tkAnthropicTLSFingerprintDisablePrefix,
		upstreamMsg,
		responseBody,
		"re-capture canonical TLS profile then bulk ClearError accounts with this prefix",
	)
	slog.Warn("anthropic_tls_fingerprint_403_permanent_disable",
		"account_id", account.ID,
		"platform", account.Platform,
		"matched_keyword", matched,
		"action", "ops_should_re-capture_claude_cli_tls_profile_then_bulk_recover",
		"upstream_msg", upstreamMsg,
	)
	s.handleAuthError(ctx, account, msg)
	return true
}
