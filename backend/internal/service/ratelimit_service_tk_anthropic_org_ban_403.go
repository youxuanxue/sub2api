package service

import (
	"context"
	"log/slog"
	"strings"
)

// tkAnthropicOrgBan403Keywords matches Anthropic 403 permission_error bodies that
// signal a PERMANENT organization-level block on this account's credentials —
// not transient upstream jitter. The canonical incident (2026-06-16, edge us6
// account edge-ls-oh-3-d) is the OAuth subscription relay ban:
//
//	{"type":"error","error":{"type":"permission_error",
//	  "message":"OAuth authentication is currently not allowed for this organization."}}
//
// Anthropic's sibling org-disabled verdict is permanently disabled when it
// arrives as a 400 ("organization has been disabled" → case 400 in
// HandleUpstreamErrorRaw) but can also arrive as a 403; catch that shape here
// too so the HTTP status code never decides recoverability for an
// account-fatal condition. Matching is case-insensitive substring
// (matchTempUnschedKeyword).
var tkAnthropicOrgBan403Keywords = []string{
	"not allowed for this organization",
	"organization has been disabled",
}

// tkTryDisableAnthropicOrgBan403 reports whether an Anthropic upstream 403 is a
// permanent organization-level ban and, when so, permanently disables the
// account (SetError, no auto-recovery) and fires the Feishu permanent-disable
// card — then returns true so the caller skips the transient cooldown ladder.
//
// WHY (handle403 gap, 2026-06-16 edge us6 incident): without this, an org-ban
// 403 falls through to handleAnthropicUpstreamError — the generic 3/3
// short-window ladder whose cooldown caps at 10 min and AUTO-RECOVERS. The
// account then flaps forever: cool 10m → re-enter pool → 403 → re-cool, burning
// one failover per cycle and polluting upstream-error attribution, until an
// operator manually sets schedulable=false. A genuine org ban is irrecoverable
// for THIS account/org (token refresh keeps succeeding — the grant lives, the
// org is policy-blocked at request time), so the correct action is permanent
// disable + alert to prompt manual account replacement, mirroring the 400
// "organization has been disabled" handling and the 401 grant-revocation
// escalation (tkTryEscalateRevokedOAuth401).
//
// Returns false (→ falls through to the existing tiered cooldown unchanged) when
// nothing matches, so any non-org-ban 403 behaves exactly as before. Fail-safe:
// a missing match never escalates.
func (s *RateLimitService) tkTryDisableAnthropicOrgBan403(ctx context.Context, account *Account, upstreamMsg string, responseBody []byte) bool {
	if s == nil || account == nil {
		return false
	}
	haystack := strings.ToLower(strings.TrimSpace(upstreamMsg) + " " + string(responseBody))
	matched := matchTempUnschedKeyword(haystack, tkAnthropicOrgBan403Keywords)
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
