package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Google has two very different ways to answer 429 RESOURCE_EXHAUSTED, and only
// one of them says anything about the account that served the request:
//
//  1. The account (or its project/quota) is rate limited. The provider says so
//     structurally — a RATE_LIMIT_EXCEEDED ErrorInfo reason, a QuotaFailure, or
//     an explicit retry hint. Cooling the account is correct: the next request
//     should go to a peer.
//
//  2. The rejection is about the request itself — content, prompt shape, or a
//     shared capacity pool. The account is fine; another account would be hit by
//     the same rule.
//
// Cooling on case 2 is actively harmful because it is applied per account while
// the cause is per request: one rejected request walks the failover chain and
// writes a model cooldown on every account it touches, taking out the accounts
// that could have served the *next* request. That is how a single bad payload
// can degrade a whole group.
//
// RATE_LIMIT_EXCEEDED and MODEL_CAPACITY_EXHAUSTED are already handled earlier
// in handleUpstreamError, so this predicate only has to decide the remaining
// "unclassified 429" bucket: cool only when there is positive evidence of an
// account-scoped limit.

// tkGoogle429AccountLimitReason is the ErrorInfo reason Google attaches when the
// account's own quota/rate limit is the cause.
const tkGoogle429AccountLimitReason = "RATE_LIMIT_EXCEEDED"

// tkGoogle429CapacityReasons name conditions that are shared across accounts.
// Rotating or cooling on these cannot help, and would spread the penalty.
var tkGoogle429CapacityReasons = []string{
	"MODEL_CAPACITY_EXHAUSTED",
	"SHARED_CAPACITY_EXHAUSTED",
	"UNIFIED_CAPACITY_EXHAUSTED",
}

// tkGoogle429ResetHintFields are the places Google puts an explicit "retry after
// this long" hint. A hint is account-scoped corroboration: the provider is
// telling us this caller is over a limit and when it clears, which is exactly
// the case where cooling the account is correct and useful.
//
// quotaResetDelay also covers the legacy envelope our own
// ParseGeminiRateLimitResetTime consumes (details[].metadata.quotaResetDelay),
// so an untyped 429 that still carries a reset hint keeps cooling as before.
var tkGoogle429ResetHintFields = []string{
	"metadata.quotaResetDelay",
	"retryDelay",
	"metadata.retryDelay",
}

// Only Google's typed quota verdict justifies immediately abandoning a free attempt.
func tkAntigravityQuotaDetail(body []byte) gjson.Result {
	if gjson.GetBytes(body, "error.status").String() == googleRPCStatusResourceExhausted {
		for _, detail := range gjson.GetBytes(body, "error.details").Array() {
			if detail.Get("@type").String() == googleRPCTypeErrorInfo && detail.Get("reason").String() == "QUOTA_EXHAUSTED" {
				return detail
			}
		}
	}
	return gjson.Result{}
}

// tkGoogle429CorroboratesAccountLimit reports whether a 429 carries provider
// evidence that THIS account is over its own limit, as opposed to the request or
// a pool shared by every account.
//
// The bias is deliberately strict: when the evidence is ambiguous we return
// false and let the caller fail over without cooling. An un-cooled account is
// retried once and self-corrects; an over-cooled account silently removes
// capacity for the coolsdown duration, which is far more expensive.
func tkGoogle429CorroboratesAccountLimit(body []byte) bool {
	if tkAntigravityQuotaDetail(body).Exists() {
		return true
	}
	details := gjson.GetBytes(body, "error.details")
	if !details.IsArray() {
		// Some envelopes arrive double-wrapped ({"0": {...}}).
		details = gjson.GetBytes(body, "0.error.details")
	}
	if !details.IsArray() || len(details.Array()) == 0 {
		// No structured evidence at all: the shared "Resource exhausted, please
		// try again later" shape. Treat as request/capacity scoped.
		return false
	}
	for _, detail := range details.Array() {
		// A QuotaFailure names the metric that ran out. It is account scoped by
		// definition, so it counts as corroboration. Checked before the reason
		// lookup because QuotaFailure carries no top-level reason field.
		if strings.Contains(strings.ToLower(detail.Get("@type").String()), "quotafailure") {
			return true
		}
		reason := strings.ToUpper(strings.TrimSpace(detail.Get("reason").String()))
		if reason == "" {
			continue
		}
		if containsStringFold(tkGoogle429CapacityReasons, reason) {
			return false
		}
		if reason == tkGoogle429AccountLimitReason {
			return true
		}
	}
	// No reason field (or only unknown reasons), but the provider may still have
	// told us when this caller's limit clears. That is account-scoped evidence,
	// and it is what lets the cooldown expire at the right moment.
	for _, detail := range details.Array() {
		for _, field := range tkGoogle429ResetHintFields {
			if strings.TrimSpace(detail.Get(field).String()) != "" {
				return true
			}
		}
	}
	return false
}

// containsStringFold reports whether needle is present in haystack,
// case-insensitively. Inputs are expected to be already upper-cased reasons;
// the fold keeps the helper honest if a caller passes mixed case.
func containsStringFold(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if strings.EqualFold(candidate, needle) {
			return true
		}
	}
	return false
}
