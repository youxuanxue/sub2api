package service

import (
	"context"
	"log/slog"
	"strings"
)

var tkAnthropicOrgBan403Keywords = []string{
	"authentication is currently not allowed for this organization",
	"organization has been disabled",
}

func tkMatchAnthropicOrgBan403Body(upstreamMsg string, responseBody []byte) string {
	haystack := strings.ToLower(strings.TrimSpace(upstreamMsg) + " " + string(responseBody))
	return matchTempUnschedKeyword(haystack, tkAnthropicOrgBan403Keywords)
}

func (s *RateLimitService) tkTryDisableAnthropicOrgBan403(ctx context.Context, account *Account, upstreamMsg string, responseBody []byte) bool {
	if s == nil || account == nil {
		return false
	}
	matched := tkMatchAnthropicOrgBan403Body(upstreamMsg, responseBody)
	if matched == "" {
		return false
	}

	msg := buildForbiddenErrorMessage(
		"Organization OAuth ban (403):",
		upstreamMsg,
		responseBody,
		"OAuth authentication is currently not allowed for this organization",
	)

	// greppable marker (§8.5 troubleshooting convention) — distinct from the
	// transient anthropic_upstream_error ladder and the TLS-fingerprint signal,
	// so ops alerting can route this to "replace the account", not "wait it out".
	slog.Warn("anthropic_org_ban_403_permanent_disable",
		"account_id", account.ID,
		"platform", account.Platform,
		"matched_keyword", matched,
		"action", "ops_should_replace_with_unbanned_account",
		"upstream_msg", upstreamMsg,
	)
	s.handleAuthError(ctx, account, msg)
	return true
}
