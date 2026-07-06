package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// TK G4 — model-dimension cooldown for Anthropic per-class sub-bucket 429s.
//
// Background (prod 2026-06, edge us6, account edge-ls-oh-3-d): the original G4
// belief was that an Anthropic "unified 5h / 7d" window 429 was tied to the
// heaviest model class (typically opus) and that sonnet/haiku on the same
// account stayed healthy — so it scoped the cooldown to (account × model
// class) instead of pulling the whole account out of scheduling.
//
// CORRECTION (direct upstream probe, edge-us3 account a1, 2026-06-06): the
// account-level `anthropic-ratelimit-unified-5h-*` / `-7d-*` headers (NO
// model-class suffix) are ACCOUNT-WIDE SHARED windows. With
// `unified-7d-status: rejected` (utilization 1.0, surpassed-threshold 1.0) the
// account returned HTTP 429 rate_limit_error to a *sonnet* request even though
// `unified-7d_sonnet-status: allowed` / utilization 0.0 — i.e. a sibling
// class's own sub-bucket having headroom does NOT let it through when the
// overall window is rejected (`unified-status: rejected`,
// `representative-claim: seven_day`). So the original premise is FALSE for an
// account-wide window: exhausting it 429s every class regardless of per-class
// sub-bucket. Cooling only the requested class there leaves sibling classes
// (e.g. haiku) falsely "schedulable" and never marks the account rate-limited.
//
// Corrected behaviour: model-scope the cooldown ONLY when the 429 is NOT an
// account-wide window exhaustion — i.e. a genuine per-class sub-bucket limit
// (anthropic-ratelimit-unified-7d_<class>-status rejected while the overall
// 5h/7d window is still allowed). When an account-wide window (5h or 7d) is
// the surpassed one (anthropic429Result.window == "5h"/"7d"), this helper
// declines (returns false) and the caller falls through to account-level
// SetRateLimited so ALL classes are cooled until reset — matching the upstream
// `unified-status: rejected` semantics. Hard account failures (401/403 auth,
// credit balance, org disabled, KYC, 529 overloaded, fallback no-reset 429)
// are not model-related and keep their account-level behaviour unchanged.
//
// Convergence: model-scoping (when it does apply) reuses the existing
// per-(account × scope) cooldown mechanism already in
// account.Extra["model_rate_limits"] — the same store written by
// SetModelRateLimit (404 model-not-found, antigravity credits, gemini
// code-assist) and read by the scheduler's IsSchedulableForModelWithContext →
// isModelRateLimitedWithContext gate. The store is durable (accounts.extra
// JSONB + scheduler snapshot via outbox) and self-expiring (reads compare
// time.Now() against rate_limit_reset_at).

const (
	// anthropicModelClassRateLimitPrefix namespaces the model-class cooldown
	// scope keys so they never collide with exact-model-name scope keys
	// written by the 404 / model-not-found path.
	anthropicModelClassRateLimitPrefix = "anthropic:class:"

	anthropicModelClassOpus    = "opus"
	anthropicModelClassSonnet  = "sonnet"
	anthropicModelClassHaiku   = "haiku"
	anthropicModelClassFable   = "fable"
	anthropicModelClassUnknown = ""

	// tkAnthropicModelCooldownReason is recorded on the model_rate_limits
	// entry so operators / debug tooling can distinguish a unified-window
	// model-class cooldown from a 404 model-not-found cooldown.
	tkAnthropicModelCooldownReason = "anthropic_unified_window_exceeded"

	// anthropicUnifiedWindow5h / 7d are the account-wide (non-class-suffixed)
	// unified window labels set on anthropic429Result.window by
	// calculateAnthropic429ResetTime when the corresponding
	// anthropic-ratelimit-unified-{5h,7d}-* header is surpassed. They mirror the
	// literal strings that parser uses; an empty window means no account-wide
	// window was surpassed (a genuine per-class sub-bucket limit), the only case
	// where model-scoping is correct. See the package doc above for the
	// edge-us3 upstream probe that established these are account-wide.
	anthropicUnifiedWindow5h = "5h"
	anthropicUnifiedWindow7d = "7d"
)

// tkAnthropicModelClass normalizes an Anthropic model name to its capacity
// class (opus / sonnet / haiku / fable). Returns "" when the class can't be
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
	case strings.Contains(name, "fable"):
		return anthropicModelClassFable
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
// class) cooldown for a genuine per-class Anthropic sub-bucket 429 instead of
// an account-level SetRateLimited.
//
// It returns true ONLY when the cooldown was written at model-class scope —
// i.e. this is an Anthropic account, the 429 is NOT an account-wide unified
// window exhaustion (result.window is empty, so a per-class sub-bucket is the
// binding limit), AND the requested model resolves to a known class. In that
// case the caller MUST NOT also call account-level SetRateLimited. It still
// counts as an authoritative upstream cooldown for the purpose of suppressing
// the 3/3 ladder write, so the caller treats a true return like the
// account-level rate-limit path.
//
// It returns false in every other case — crucially including when an
// ACCOUNT-WIDE unified window (5h or 7d) is the surpassed one
// (result.window == "5h"/"7d"): such windows are shared across all model
// classes (edge-us3 upstream probe, 2026-06-06 — see package doc), so the
// caller MUST fall through to account-level SetRateLimited to cool every
// class. Also returns false for non-Anthropic, unknown/absent model class, or
// repo write failure.
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
	// Account-wide unified window (5h/7d) exhaustion blocks EVERY model class
	// regardless of per-class sub-bucket headroom — model-scoping it would
	// leave sibling classes falsely schedulable and never mark the account
	// rate-limited. Decline so the caller does account-level SetRateLimited.
	// Only an empty window (genuine per-class sub-bucket limit) is model-scoped.
	if result.window == anthropicUnifiedWindow5h || result.window == anthropicUnifiedWindow7d {
		return false
	}
	class := tkAnthropicModelClass(requestedModel)
	if class == anthropicModelClassFable {
		// Fable has a dedicated upstream 7d_oi window and family scope
		// (anthropicFableRateLimitKey). Without that header signal, keep the
		// legacy account-level 429 path instead of inventing a generic class
		// cooldown from a healthy 5h/7d response.
		return false
	}
	scopeKey := tkAnthropicModelClassScopeKey(class)
	if scopeKey == "" {
		// Unknown model class (or no model context at the call site): cannot
		// safely narrow the cooldown — fall back to account-level so we never
		// leave an exhausted window uncooled.
		return false
	}

	resetAt := result.resetAt
	// detail = 模型类[·窗口]，让飞书摘要直接显示被限流的是哪个模型类与上游用量窗口。
	s.notifyAccountSchedulingBlocked(account, resetAt, "429_model_class", tkAnthropicModelCooldownDetail(class, result))
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scopeKey, resetAt, tkAnthropicModelCooldownReason); err != nil {
		slog.Warn("anthropic_model_class_rate_limit_failed",
			"account_id", account.ID,
			"scope", scopeKey,
			"error", err)
		return false
	}

	// NOTE: we intentionally do NOT write the account-global 5h session window
	// ("rejected") here. This path now only runs for a genuine per-class
	// sub-bucket limit (result.window == "", i.e. neither account-wide 5h nor
	// 7d window was surpassed), so the account's shared 5h window is NOT
	// rejected — forcing it would corrupt the Setup-Token usage gauge. The
	// account-wide window path (caller's account-level branch) still calls
	// tkUpdateAnthropic5hSessionWindow when an overall window is the trigger.
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

// tkAnthropicWindowLabel renders the unified usage window that triggered an
// Anthropic 429 as a human-facing Feishu digest detail ("5h 窗口" / "7d 窗口"),
// or "" when the window can't be determined. This is the operator-facing answer
// to "which upstream dimension exceeded its limit" for account-level 429
// cooldowns — it is the upstream usage window, NOT a TK internal cap.
func tkAnthropicWindowLabel(result *anthropic429Result) string {
	if result == nil || result.window == "" {
		return ""
	}
	return result.window + " 窗口"
}

// tkAnthropicModelCooldownDetail renders the model-class 429 cooldown digest
// detail as "<class>·<window> 窗口" (e.g. "opus·5h 窗口"), degrading to just the
// class when the window is unknown, or to just the window when the class is
// unknown. Empty when neither is known.
func tkAnthropicModelCooldownDetail(class string, result *anthropic429Result) string {
	if class == anthropicModelClassUnknown {
		return tkAnthropicWindowLabel(result)
	}
	if w := tkAnthropicWindowLabel(result); w != "" {
		return class + "·" + w
	}
	return class
}

// ----------------------------------------------------------------------------
// OpenAI/Codex per-model metered sub-limit cooldown (e.g. GPT-5.3-Codex-Spark)
// ----------------------------------------------------------------------------
//
// A ChatGPT/Codex OAuth account has TWO independent rate-limit dimensions:
//   1. the account-wide 5h/7d window — the x-codex-primary/secondary-* response
//      headers, normalized into codex_5h/7d_used_percent; and
//   2. per-model METERED windows (the /wham/usage additional_rate_limits[],
//      e.g. "GPT-5.3-Codex-Spark"), each with its own 5h/7d sub-window.
//
// The spark sub-window can hit 100% (upstream 429 usage_limit_reached) while the
// account-wide window is at ~4% (prod 2026-06, account GPT-pro1: spark 5h=0%
// remaining while codex_5h_used_percent=4%). Cooling the WHOLE account on that
// 429 idles the account's still-healthy general-model capacity and, because the
// dominant traffic IS spark, empties the pool into "No available accounts"
// (the operator-reported "刷不出 codex 额度"; 12h prod: ~8k empty-pool 429s).
//
// This narrows such a 429 to a (account × model) cooldown — reusing the same
// account.Extra["model_rate_limits"] store the scheduler already consults via
// IsSchedulableForModelWithContext → isModelRateLimitedWithContext →
// modelRateLimitKeysForRequest (which, for OpenAI, checks the
// GetMappedModel(requestedModel) scope key). So writing the cooldown at the
// mapped model scope is honored with NO read-side change: a future spark request
// is skipped, while gpt-5.4 / other models on the same account still schedule
// and spark fails over to accounts whose spark sub-window has headroom.
//
// Account-wide window exhaustion is NOT (re-)detected here. It is owned by
// calculateOpenAI429ResetTime (path 1), which returns a whole-account reset from
// the ACTUAL used-percent — when a general 5h/7d window reads >=100%. This helper
// runs solely in handle429's body path (path 2), reached precisely when path 1
// declined, so it simply model-scopes the spark 429 that path 1 did not treat as
// account-wide. We deliberately do NOT add a second, lower "is the account-wide
// window healthy enough" threshold: there is no evidence the upstream enforces
// the account-wide limit below 100%, and gating a serving decision on an
// unverified threshold is a guess, not a fact (path 1's >=100% check is the only
// fact-based account-wide signal; if the x-codex headers ever reflected the spark
// window rather than the account-wide one, a 100% reading is still caught by
// path 1 → whole account).

const (
	// tkOpenAICodexMeteredCooldownReason marks a model_rate_limits entry as a
	// codex per-model metered sub-limit cooldown, distinct from the whole-account
	// rate_limit_reset_at and from the 404 / image-generation scope keys.
	tkOpenAICodexMeteredCooldownReason = "openai_codex_metered_sublimit_exceeded"
)

// tkIsOpenAICodexMeteredModel reports whether a codex model carries its own
// per-model metered sub-limit (the /wham/usage additional_rate_limits family)
// independent of the account-wide window. Today that is the "spark" tier.
// Substring + lowercase so it survives version/date/provider prefixes
// (e.g. "gpt-5.3-codex-spark").
func tkIsOpenAICodexMeteredModel(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(name, "codex") && strings.Contains(name, "spark")
}

// tkShouldOpenAICodex429BeModelScoped reports whether an OpenAI OAuth 429 should
// narrow to (account × model) cooldown instead of whole-account penalties.
// Preconditions mirror handle429 body path (path 2) before account-level
// SetRateLimited: path 1 declined (no >=100% account-wide window in headers),
// body carries a parseable usage_limit_reached reset, and the mapped model is a
// codex metered sub-limit (spark). Used by the OpenAI gateway runtime-block
// fast-path so spark sub-window 429s do not whole-account BlockAccountScheduling.
func tkShouldOpenAICodex429BeModelScoped(account *Account, headers http.Header, responseBody []byte, requestedModel string) bool {
	if account == nil || account.Platform != PlatformOpenAI || !account.IsOAuth() {
		return false
	}
	if calculateOpenAI429ResetTime(headers) != nil {
		return false
	}
	if parseOpenAIRateLimitResetTime(responseBody) == nil {
		return false
	}
	scopeKey := strings.TrimSpace(account.GetMappedModel(requestedModel))
	if scopeKey == "" || !tkIsOpenAICodexMeteredModel(scopeKey) {
		return false
	}
	return true
}

// tkTryOpenAICodexModelScopedCooldown narrows a codex per-model metered (spark)
// 429 to a (account × model) cooldown instead of the whole-account
// SetRateLimited. Mirrors tkTryAnthropicModelScopedCooldown: returns true ONLY
// when the model-scoped cooldown was written (caller MUST NOT also call
// account-level SetRateLimited), and still counts as an authoritative upstream
// cooldown so the caller suppresses the 3/3 ladder write.
//
// It returns true ONLY when BOTH hold:
//   - openai OAuth account (direct chatgpt.com; mirror apikey relay accounts use
//     a relayed error shape and fall through to whole-account);
//   - the mapped model is a codex metered model (tkIsOpenAICodexMeteredModel).
//
// Account-wide exhaustion need not be re-checked here — path 1
// (calculateOpenAI429ResetTime) already routed those (general window >=100%) to
// the whole-account path before this helper is reached. Otherwise it returns
// false and the caller keeps the existing whole-account cooldown — covering
// non-metered models, mirror accounts, absent model context (WS fast-path), and
// repo write failure.
func (s *RateLimitService) tkTryOpenAICodexModelScopedCooldown(
	ctx context.Context,
	account *Account,
	requestedModel string,
	resetAt time.Time,
) bool {
	if s == nil || s.accountRepo == nil || account == nil {
		return false
	}
	if account.Platform != PlatformOpenAI || !account.IsOAuth() {
		return false
	}
	// Canonical scope key = the mapped model the scheduler will look up
	// (modelRateLimitKeysForRequest uses GetMappedModel). Applying GetMappedModel
	// here is idempotent for already-mapped inputs (callers pass either the raw
	// requested or the mapped model), so write-key and read-key always agree.
	scopeKey := strings.TrimSpace(account.GetMappedModel(requestedModel))
	if scopeKey == "" || !tkIsOpenAICodexMeteredModel(scopeKey) {
		return false
	}

	s.notifyAccountSchedulingBlocked(account, resetAt, "429_codex_metered", scopeKey)
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scopeKey, resetAt, tkOpenAICodexMeteredCooldownReason); err != nil {
		slog.Warn("openai_codex_metered_rate_limit_failed",
			"account_id", account.ID,
			"scope", scopeKey,
			"error", err)
		return false
	}
	slog.Info("openai_codex_metered_rate_limited",
		"account_id", account.ID,
		"scope", scopeKey,
		"requested_model", requestedModel,
		"reset_at", resetAt,
		"reset_in", time.Until(resetAt).Truncate(time.Second))
	return true
}
