package service

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// TK G4 — model-dimension cooldown for Anthropic unified-window 429s.
//
// Background (prod 2026-06, edge us6, account edge-ls-oh-3-d): Anthropic
// subscription (OAuth) accounts share a rolling "unified 5h / 7d" usage
// window across ALL model classes. When the heaviest class (typically opus)
// burns through the 5h window, upstream returns a real 429 rate_limit_error
// and handle429 parses the anthropic-ratelimit-unified-5h-* headers
// (is_5h_exceeded=true / reset_5h=<unix>). The legacy behaviour then called
// SetRateLimited, which is ACCOUNT-LEVEL: it pulled the whole account out of
// scheduling until reset. But sonnet/haiku on that same account were still
// perfectly healthy — only opus's slice of the 5h window was exhausted. The
// account-level cooldown amplified "opus window full" into "entire account
// offline", wasting healthy sonnet/haiku capacity for ~1h.
//
// G4 fix: when the 429 is provably tied to a model class (5h/7d unified
// window exceeded AND we know the requested model's class), scope the
// cooldown to (account × model class) instead of the whole account. The opus
// class is cooled until reset; sonnet/haiku on the same account keep
// scheduling. Hard account failures (401/403 auth, credit balance, org
// disabled, KYC, 529 overloaded, fallback no-reset 429) are NOT model-related
// and keep their account-level behaviour unchanged.
//
// Convergence: this reuses the existing per-(account × scope) cooldown
// mechanism already in account.Extra["model_rate_limits"] — the same store
// written by SetModelRateLimit (404 model-not-found, antigravity credits,
// gemini code-assist) and read by the scheduler's
// IsSchedulableForModelWithContext → isModelRateLimitedWithContext gate. We
// add a model-CLASS scope key (anthropic:class:opus|sonnet|haiku) mirroring
// the antigravity "antigravity:gemini" family-key precedent, so no new
// repository interface method, no parallel Redis layer, and no second source
// of truth the scheduler must learn to consult. The store is durable
// (accounts.extra JSONB + scheduler snapshot via outbox) and self-expiring
// (reads compare time.Now() against rate_limit_reset_at), exactly matching
// the "transient state with a reset time" shape this feature needs.

const (
	// anthropicModelClassRateLimitPrefix namespaces the model-class cooldown
	// scope keys so they never collide with exact-model-name scope keys
	// written by the 404 / model-not-found path.
	anthropicModelClassRateLimitPrefix = "anthropic:class:"

	anthropicModelClassOpus    = "opus"
	anthropicModelClassSonnet  = "sonnet"
	anthropicModelClassHaiku   = "haiku"
	anthropicModelClassUnknown = ""

	// tkAnthropicModelCooldownReason is recorded on the model_rate_limits
	// entry so operators / debug tooling can distinguish a unified-window
	// model-class cooldown from a 404 model-not-found cooldown.
	tkAnthropicModelCooldownReason = "anthropic_unified_window_exceeded"
)

// tkAnthropicModelClass normalizes an Anthropic model name to its capacity
// class (opus / sonnet / haiku). Returns "" when the class can't be
// determined — callers MUST treat unknown as "not model-scoped" and fall
// back to account-level handling rather than guessing.
//
// Matching is substring-based and case-insensitive so it survives version
// suffixes, [1m] context tags, date stamps, and provider prefixes
// (e.g. "claude-opus-4-8[1m]", "anthropic/claude-3-5-haiku-20241022").
func tkAnthropicModelClass(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return anthropicModelClassUnknown
	}
	switch {
	case strings.Contains(name, "opus"):
		return anthropicModelClassOpus
	case strings.Contains(name, "sonnet"):
		return anthropicModelClassSonnet
	case strings.Contains(name, "haiku"):
		return anthropicModelClassHaiku
	default:
		return anthropicModelClassUnknown
	}
}

// tkAnthropicModelClassScopeKey builds the model_rate_limits scope key for a
// model class, or "" if the class is unknown.
func tkAnthropicModelClassScopeKey(class string) string {
	if class == anthropicModelClassUnknown {
		return ""
	}
	return anthropicModelClassRateLimitPrefix + class
}

// tkAnthropicModelClassScopeKeyForModel resolves a requested model name to its
// model-class scope key, or "" when the class can't be determined.
func tkAnthropicModelClassScopeKeyForModel(model string) string {
	return tkAnthropicModelClassScopeKey(tkAnthropicModelClass(model))
}

// tkTryAnthropicModelScopedCooldown attempts to write a (account × model
// class) cooldown for an Anthropic unified-window 429 instead of an
// account-level SetRateLimited.
//
// It returns true ONLY when the cooldown was written at model-class scope —
// i.e. this is an Anthropic account, the 429 is a unified-window-exceeded
// signal, AND the requested model resolves to a known class. In that case the
// caller MUST NOT also call account-level SetRateLimited (that would re-create
// the very amplification G4 removes). It still counts as an authoritative
// upstream cooldown for the purpose of suppressing the 3/3 ladder write, so
// the caller treats a true return like the account-level rate-limit path.
//
// It returns false in every other case (non-Anthropic, no model class known,
// or repo write failure), and the caller falls back to the existing
// account-level behaviour — preserving correctness for hard account failures
// and for the no-model-context call sites.
func (s *RateLimitService) tkTryAnthropicModelScopedCooldown(
	ctx context.Context,
	account *Account,
	requestedModel string,
	result *anthropic429Result,
) bool {
	if s == nil || s.accountRepo == nil || account == nil || result == nil {
		return false
	}
	if account.Platform != PlatformAnthropic {
		return false
	}
	scopeKey := tkAnthropicModelClassScopeKeyForModel(requestedModel)
	if scopeKey == "" {
		// Unknown model class (or no model context at the call site): cannot
		// safely narrow the cooldown — fall back to account-level so we never
		// leave an exhausted window uncooled.
		return false
	}

	resetAt := result.resetAt
	s.notifyAccountSchedulingBlocked(account, resetAt, "429_model_class")
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scopeKey, resetAt, tkAnthropicModelCooldownReason); err != nil {
		slog.Warn("anthropic_model_class_rate_limit_failed",
			"account_id", account.ID,
			"scope", scopeKey,
			"error", err)
		return false
	}

	// The unified 5h window is exhausted at the ACCOUNT level (it's the shared
	// window across all model classes — that's exactly why upstream 429'd).
	// Even though the cooldown is scoped to the model class for SCHEDULING, the
	// account's 5h-window state is account-global truth and must still be
	// recorded, so the Setup-Token usage gauge (estimateSetupTokenUsage, fed by
	// SessionWindow*) keeps reflecting reality. This mirrors the account-level
	// path's UpdateSessionWindow write — only the cooldown scope changed, not
	// the window signal.
	s.tkUpdateAnthropic5hSessionWindow(ctx, account.ID, result)

	slog.Info("anthropic_model_class_rate_limited",
		"account_id", account.ID,
		"scope", scopeKey,
		"requested_model", requestedModel,
		"reset_at", resetAt,
		"reset_in", time.Until(resetAt).Truncate(time.Second))
	return true
}

// tkUpdateAnthropic5hSessionWindow records the account-global 5h window as
// "rejected" from an Anthropic unified-window 429 result. Shared by the
// model-scoped cooldown path and the account-level path so the Setup-Token
// usage estimation stays consistent regardless of cooldown scope. Prefers the
// precise 5h-reset header; otherwise back-derives the window from resetAt.
func (s *RateLimitService) tkUpdateAnthropic5hSessionWindow(ctx context.Context, accountID int64, result *anthropic429Result) {
	if s == nil || s.accountRepo == nil || result == nil {
		return
	}
	windowEnd := result.resetAt
	if result.fiveHourReset != nil {
		windowEnd = *result.fiveHourReset
	}
	windowStart := windowEnd.Add(-5 * time.Hour)
	if err := s.accountRepo.UpdateSessionWindow(ctx, accountID, &windowStart, &windowEnd, "rejected"); err != nil {
		slog.Warn("rate_limit_update_session_window_failed", "account_id", accountID, "error", err)
	}
}

// tkAnthropicModelClassRateLimitActive reports whether the account is in a
// model-class cooldown for the given requested model. Mirrors the antigravity
// family-key check in isModelRateLimitedWithContext: the scheduler consults
// this in addition to the exact-model-name key so a class-scoped cooldown
// (opus) keeps sibling classes (sonnet/haiku) schedulable.
func (a *Account) tkAnthropicModelClassRateLimitActive(requestedModel string) bool {
	if a == nil || a.Platform != PlatformAnthropic {
		return false
	}
	scopeKey := tkAnthropicModelClassScopeKeyForModel(requestedModel)
	if scopeKey == "" {
		return false
	}
	return a.isRateLimitActiveForKey(scopeKey)
}

// tkAnthropicModelClassRateLimitRemaining returns the remaining cooldown for
// the account's model-class scope of the given requested model (0 if none).
func (a *Account) tkAnthropicModelClassRateLimitRemaining(requestedModel string) time.Duration {
	if a == nil || a.Platform != PlatformAnthropic {
		return 0
	}
	scopeKey := tkAnthropicModelClassScopeKeyForModel(requestedModel)
	if scopeKey == "" {
		return 0
	}
	return a.getRateLimitRemainingForKey(scopeKey)
}
