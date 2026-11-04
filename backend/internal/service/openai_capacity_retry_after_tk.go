package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// openAICapacityRetryAfterModelCooldownCap bounds how long one upstream
// capacity Retry-After may keep a single account+model pair out of selection.
// server_is_overloaded Retry-After (often 301-330s) is advice for this request,
// not an account quota window. 60s avoids an immediate rehit without parking a
// small pool for the whole hint. Quota 429 cooldowns stay on their own owner.
// The in-request same-account retry budget stays on maxRequestScopedRetryDelay
// and must not sleep for the upstream delay.
const openAICapacityRetryAfterModelCooldownCap = 60 * time.Second

const openAICapacityRetryAfterModelCooldownReason = "openai_capacity_retry_after"

// openAICapacityErrorHonorsRetryAfter reports capacity signals whose
// Retry-After describes this account+model, not a whole-account outage.
// server_is_overloaded / slow_down / overloaded wording share the stream
// capacity detector; HTTP 529 is the overload status even when the body
// omits that code.
func openAICapacityErrorHonorsRetryAfter(statusCode int, message string, payload []byte) bool {
	return statusCode == 529 || isOpenAIRequestScopedCapacityShed(message, payload)
}

// openAICapacityRetryAfterDuration reads a delay from the HTTP Retry-After
// header or the error envelope's headers.retry-after. Missing, non-positive,
// and unparseable values return false so callers keep today's behavior.
func openAICapacityRetryAfterDuration(headers http.Header, payload []byte, now time.Time) (time.Duration, bool) {
	if now.IsZero() {
		now = time.Now()
	}
	if resetAt := parseRetryAfterResetTime(headers, now); resetAt != nil && resetAt.After(now) {
		return capOpenAICapacityRetryAfter(resetAt.Sub(now)), true
	}
	raw := openAIErrorEnvelopeRetryAfter(payload)
	if raw == "" {
		return 0, false
	}
	header := http.Header{}
	header.Set("Retry-After", raw)
	resetAt := parseRetryAfterResetTime(header, now)
	if resetAt == nil || !resetAt.After(now) {
		return 0, false
	}
	return capOpenAICapacityRetryAfter(resetAt.Sub(now)), true
}

func capOpenAICapacityRetryAfter(delay time.Duration) time.Duration {
	if delay > openAICapacityRetryAfterModelCooldownCap {
		return openAICapacityRetryAfterModelCooldownCap
	}
	return delay
}

func openAIErrorEnvelopeRetryAfter(payload []byte) string {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ""
	}
	for _, path := range []string{"error.headers", "response.error.headers"} {
		headers := gjson.GetBytes(payload, path)
		if !headers.Exists() || !headers.IsObject() {
			continue
		}
		var found string
		headers.ForEach(func(key, value gjson.Result) bool {
			if !strings.EqualFold(strings.TrimSpace(key.String()), "retry-after") {
				return true
			}
			found = strings.TrimSpace(value.String())
			return found == ""
		})
		if found != "" {
			return found
		}
	}
	return ""
}

// tkApplyOpenAICapacityRetryAfterModelCooldown persists a bounded
// account+model cooldown when a capacity error carries Retry-After and no
// longer model cooldown is already in effect. It does not temp-unschedule or
// overload the account, and it does not sleep.
func (s *OpenAIGatewayService) tkApplyOpenAICapacityRetryAfterModelCooldown(
	ctx context.Context,
	account *Account,
	statusCode int,
	headers http.Header,
	payload []byte,
	message string,
	canonicalModel ...string,
) bool {
	if s == nil || s.rateLimitService == nil || s.rateLimitService.accountRepo == nil || account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	if !openAICapacityErrorHonorsRetryAfter(statusCode, message, payload) {
		return false
	}
	model := strings.TrimSpace(firstNonEmpty(canonicalModel...))
	if model == "" {
		return false
	}
	now := time.Now()
	delay, ok := openAICapacityRetryAfterDuration(headers, payload, now)
	if !ok || delay <= 0 {
		return false
	}
	resetAt := now.Add(delay)
	// A model rule (or an earlier write) that already lasts at least this long
	// stays the only cooldown. Do not stack a second, longer window on top.
	if existing := account.modelRateLimitResetAt(model); existing != nil && !existing.Before(resetAt) {
		return false
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	if err := s.rateLimitService.accountRepo.SetModelRateLimit(stateCtx, account.ID, model, resetAt, openAICapacityRetryAfterModelCooldownReason); err != nil {
		slog.Warn("openai_capacity_retry_after_model_cooldown_failed",
			"account_id", account.ID,
			"model", model,
			"status_code", statusCode,
			"error", err)
		return false
	}
	setAccountModelRateLimitSnapshot(account, model, resetAt, openAICapacityRetryAfterModelCooldownReason, now)
	slog.Info("openai_capacity_retry_after_model_cooldown",
		"account_id", account.ID,
		"model", model,
		"status_code", statusCode,
		"reset_at", resetAt,
		"cooldown_seconds", int(delay.Seconds()))
	return true
}

// appendOpenAICapacityRetryAfterWireAliases lets a cooldown stored on the
// client model (gpt-5.4) block selection after that name is canonicalized to
// its Codex wire id (gpt-5.6-terra). Only this reason participates, so other
// model cooldowns keep exact-key matching. gpt-5.1 stays on a different wire.
func appendOpenAICapacityRetryAfterWireAliases(account *Account, keys []string, modelKey string) []string {
	if account == nil || account.Extra == nil || modelKey == "" {
		return keys
	}
	rawLimits, ok := account.Extra[modelRateLimitsKey].(map[string]any)
	if !ok || len(rawLimits) == 0 {
		return keys
	}
	wire := CanonicalizeOpenAICompatRoutingModel(modelKey)
	if wire == "" {
		wire = modelKey
	}
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		seen[key] = struct{}{}
	}
	for stored, raw := range rawLimits {
		if _, exists := seen[stored]; exists {
			continue
		}
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		reason, _ := entry["reason"].(string)
		if reason != openAICapacityRetryAfterModelCooldownReason {
			continue
		}
		storedWire := CanonicalizeOpenAICompatRoutingModel(stored)
		if storedWire == "" {
			storedWire = stored
		}
		if storedWire != wire {
			continue
		}
		keys = append(keys, stored)
		seen[stored] = struct{}{}
	}
	return keys
}
