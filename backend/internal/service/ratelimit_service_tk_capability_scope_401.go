package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// TK (prod P0 2026-06-13, GPT专线 group_id=2): a capability/scope-level upstream
// 401 — the upstream rejects a SPECIFIC capability (e.g. image generation)
// because the serving OAuth token is missing that capability's scope — must NOT
// be treated as an account-level auth failure.
//
// Incident: the GPT专线 group had only 2 healthy OpenAI OAuth accounts (ids 9
// "GPT-pro1", 48 "GPT-pro2"). Both lacked the image-generation scope. A
// `gpt-image-1` request (POST /v1/images/generations) made OpenAI return:
//
//	OAuth 401: You have insufficient permissions for this operation. Missing
//	scopes: api.model.images.request. Check that you have the correct role in
//	your organization (Reader, Writer, Owner) and project (Member, Owner) ...
//
// The generic 401 handling cooled the WHOLE account for ~10 minutes
// (SetTempUnschedulable / runtime block), which drained BOTH healthy accounts
// out of the pool for ALL models (claude-opus-4-7, gpt-5.5, opus-4-8, gpt-image-*).
// The entire group then served 429 "No available accounts" for the cooldown
// window — a classic thin-pool 503/429 amplifier (cf. #725): one poison
// capability gets fanned out into a pool-wide outage.
//
// The account is perfectly capable of serving chat/completions — only the IMAGE
// capability is unauthorized. So this sub-class of 401 must:
//  1. NOT cool / disable the account (HandleUpstreamError returns shouldDisable=false),
//  2. NOT trigger account failover (shouldFailoverOpenAIUpstreamResponse=false) —
//     every account in the pool shares the same missing scope, so failing over
//     just poisons each account in turn,
//  3. surface a clear client-facing 400 for THAT request only, analogous to how
//     TK returns 400 for retired/unservable model names.
//
// A GENUINE account-level 401 (invalid/revoked credentials, expired token,
// {"detail":"Unauthorized"}, token_invalidated/token_revoked) keeps the existing
// cooldown/disable behavior — the whole point is to DISTINGUISH the two, never to
// weaken real auth-failure handling.
//
// Kept in a TK-only companion file so future upstream merges of
// ratelimit_service.go / openai_gateway_service.go do not collide on this branch.

// tkCapabilityScope401Marker is the OpenAI substring that unambiguously marks a
// capability/scope rejection (case-insensitive). "missing scopes" is the
// load-bearing signal: OpenAI emits it only when a token lacks a scope for a
// specific capability (e.g. api.model.images.request), never for a generic
// expired/invalid credential. "insufficient permissions for this operation"
// accompanies it and is required as a second anchor so a server-side message that
// merely happens to contain "scope" cannot trip this path.
const (
	tkCapabilityScope401MissingScopes  = "missing scopes"
	tkCapabilityScope401InsufficientOp = "insufficient permissions for this operation"
)

// tkIsCapabilityScope401 reports whether a 401 upstream response is a
// capability/scope rejection (token missing a capability scope) rather than an
// account-level auth failure. Matches the OpenAI missing-scope signature
// precisely — both anchor phrases must be present — to avoid mis-classifying a
// generic 401 as a recoverable capability gap.
func tkIsCapabilityScope401(statusCode int, body []byte) bool {
	if statusCode != 401 {
		return false
	}
	hay := tkCapabilityScope401Haystack(body)
	if hay == "" {
		return false
	}
	return strings.Contains(hay, tkCapabilityScope401MissingScopes) &&
		strings.Contains(hay, tkCapabilityScope401InsufficientOp)
}

// tkCapabilityScope401Haystack assembles the lower-cased text to scan: the
// extracted/structured error message plus the common OpenAI envelope fields. We
// scan structured fields (not the raw body verbatim) so a scope literal that only
// appears inside an unrelated field cannot match, while still covering both the
// {"error":{"message":...}} and {"detail":...} shapes OpenAI uses for 401.
func tkCapabilityScope401Haystack(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	if msg := strings.TrimSpace(extractUpstreamErrorMessage(body)); msg != "" {
		parts = append(parts, msg)
	}
	if errMsg := strings.TrimSpace(gjson.GetBytes(body, "error.message").String()); errMsg != "" {
		parts = append(parts, errMsg)
	}
	if detail := strings.TrimSpace(gjson.GetBytes(body, "detail").String()); detail != "" {
		parts = append(parts, detail)
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

// tkCapabilityScope401ClientMessage is the client-facing message for a
// capability-scope 401. It states the capability is not available on the serving
// account WITHOUT leaking which account, mirroring the actionable-but-non-leaky
// shape of TK's retired-model 400 messages.
func tkCapabilityScope401ClientMessage(upstreamMsg string) string {
	base := "The requested capability is not available on the serving account " +
		"(the upstream account's credentials are missing the required scope for this operation)."
	upstreamMsg = strings.TrimSpace(upstreamMsg)
	if upstreamMsg == "" {
		return base
	}
	return base + " Upstream detail: " + upstreamMsg
}
