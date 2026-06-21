package service

import (
	"strings"
	"time"
)

// ActiveModelCooldown is a service-internal projection of one still-active
// per-model-class rate-limit entry stored in account.Extra["model_rate_limits"].
//
// TK fifth-edge visibility: an Anthropic unified-window 429 (e.g. sonnet 5h/7d
// exhausted) is recorded on the EDGE OAuth account at scope
// "anthropic:class:sonnet" by ratelimit_service_tk_model_cooldown.go and read by
// model_rate_limit.go. That cooldown is INVISIBLE to the prod mirror account, so
// an all-green prod snapshot masks a 100%-failing sonnet class on a single-account
// edge. ActiveModelRateLimits exposes the still-active entries so the Edge Accounts
// overview (handler.toEdgeAccountDTO) can surface a per-class 限流 badge — read
// path only; it never writes or mutates Extra.
type ActiveModelCooldown struct {
	RateLimitedAt    time.Time
	RateLimitResetAt time.Time
	Reason           string
}

// ActiveModelRateLimits returns the per-scope model rate-limit entries that are
// STILL active at `now` (reset_at strictly in the future), keyed by their full
// scope (e.g. "anthropic:class:sonnet"). A lapsed cooldown is dropped so a stale
// breadcrumb never produces a phantom badge — mirroring modelRateLimitResetAt's
// RFC3339 parse contract (nil-on-error, future-only). Returns nil (not an empty
// map) when the account has no active entries, so the DTO field omits cleanly.
func (a *Account) ActiveModelRateLimits(now time.Time) map[string]ActiveModelCooldown {
	if a == nil || a.Extra == nil {
		return nil
	}
	rawLimits, ok := a.Extra[modelRateLimitsKey].(map[string]any)
	if !ok {
		return nil
	}

	var out map[string]ActiveModelCooldown
	for scope, raw := range rawLimits {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		resetAt, ok := parseRFC3339Field(entry, "rate_limit_reset_at")
		if !ok || !resetAt.After(now) {
			continue
		}
		cooldown := ActiveModelCooldown{RateLimitResetAt: resetAt}
		if limitedAt, ok := parseRFC3339Field(entry, "rate_limited_at"); ok {
			cooldown.RateLimitedAt = limitedAt
		}
		if reason, ok := entry["reason"].(string); ok {
			cooldown.Reason = strings.TrimSpace(reason)
		}
		if out == nil {
			out = make(map[string]ActiveModelCooldown, len(rawLimits))
		}
		out[scope] = cooldown
	}
	return out
}

// parseRFC3339Field reads an RFC3339 string field from a model_rate_limits entry,
// mirroring modelRateLimitResetAt's parse contract (empty / non-string / unparsable
// → ok=false). Kept local to the TK companion so the upstream-shaped read path is
// untouched.
func parseRFC3339Field(entry map[string]any, field string) (time.Time, bool) {
	raw, ok := entry[field].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
