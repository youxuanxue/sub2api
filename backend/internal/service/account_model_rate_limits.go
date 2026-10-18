package service

import (
	"strings"
	"time"
)

// ActiveModelCooldown is a service-internal projection of one active
// per-model rate-limit entry stored in account.Extra["model_rate_limits"].
type ActiveModelCooldown struct {
	RateLimitedAt    time.Time
	RateLimitResetAt time.Time
	Reason           string
}

// ActiveModelRateLimits returns entries whose reset time is still in the future.
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
		resetAt, ok := parseRFC3339ModelRateLimitField(entry, "rate_limit_reset_at")
		if !ok || !resetAt.After(now) {
			continue
		}
		cooldown := ActiveModelCooldown{RateLimitResetAt: resetAt}
		if limitedAt, ok := parseRFC3339ModelRateLimitField(entry, "rate_limited_at"); ok {
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

func parseRFC3339ModelRateLimitField(entry map[string]any, field string) (time.Time, bool) {
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
