package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

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
	if parseOpenAIRateLimitResetTime(responseBody) == nil && !isCodexBengalfoxActiveLimit(headers) {
		return false
	}
	scopeKey := strings.TrimSpace(requestedModel)
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
	// Error handlers receive the executed canonical model. Mapping it again can
	// follow a second alias or wildcard and cool a different model.
	scopeKey := strings.TrimSpace(requestedModel)
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
