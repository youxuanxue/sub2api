//go:build unit

package service

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// G4 — model-dimension cooldown for Anthropic per-class sub-bucket 429s.
//
// Corrected semantics (edge-us3 upstream probe, 2026-06-06): the account-wide
// unified 5h/7d windows (anthropic-ratelimit-unified-{5h,7d}-*, no class
// suffix) are SHARED across all model classes — exhausting one 429s every
// class even when that class's own sub-bucket is allowed. So an account-wide
// window exhaustion (anthropic429Result.window == "5h"/"7d") cools the WHOLE
// account (account-level SetRateLimited); model-scoping is reserved for a
// genuine per-class sub-bucket limit (window == "", neither overall window
// surpassed) so siblings stay schedulable only when they truly have capacity.

func tkModelClassScope(class string) string {
	return anthropicModelClassRateLimitPrefix + class
}

// --- tkAnthropicModelClass normalization boundaries ---------------------------

func TestTkAnthropicModelClass_Boundaries(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"claude-opus-4-8", anthropicModelClassOpus},
		{"claude-opus-4-8[1m]", anthropicModelClassOpus},
		{"anthropic/claude-3-opus-20240229", anthropicModelClassOpus},
		{"CLAUDE-OPUS-4-6-THINKING", anthropicModelClassOpus},
		{"claude-sonnet-4-5", anthropicModelClassSonnet},
		{"claude-3-5-sonnet-20241022", anthropicModelClassSonnet},
		{"claude-3-5-haiku-20241022", anthropicModelClassHaiku},
		{"claude-haiku-4-5", anthropicModelClassHaiku},
		{"claude-fable-5", anthropicModelClassFable},
		{"fable", anthropicModelClassFable},
		{"  claude-opus-4-8  ", anthropicModelClassOpus},
		{"", anthropicModelClassUnknown},
		{"gpt-5.4", anthropicModelClassUnknown},
		{"gemini-3-pro", anthropicModelClassUnknown},
	}
	for _, tc := range cases {
		require.Equalf(t, tc.want, tkAnthropicModelClass(tc.model),
			"model=%q", tc.model)
	}
}

func TestTkAnthropicModelClassScopeKeyForModel(t *testing.T) {
	require.Equal(t, tkModelClassScope("opus"), tkAnthropicModelClassScopeKeyForModel("claude-opus-4-8"))
	require.Equal(t, tkModelClassScope("sonnet"), tkAnthropicModelClassScopeKeyForModel("claude-sonnet-4-5"))
	require.Equal(t, tkModelClassScope("haiku"), tkAnthropicModelClassScopeKeyForModel("claude-3-5-haiku"))
	require.Equal(t, tkModelClassScope("fable"), tkAnthropicModelClassScopeKeyForModel("claude-fable-5"))
	require.Empty(t, tkAnthropicModelClassScopeKeyForModel("gpt-5.4"))
	require.Empty(t, tkAnthropicModelClassScopeKeyForModel(""))
}

// --- write side: account-wide window exceeded → account-level, NOT model-scoped --

// anthropic5hExceededHeaders models an ACCOUNT-WIDE 5h window exhaustion
// (5h utilization >= 1.0). Per the corrected semantics this must cool the
// whole account, never a single model class.
func anthropic5hExceededHeaders(resetAt int64) http.Header {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.02")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(resetAt, 10))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.30")
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(resetAt+3600*24, 10))
	return headers
}

// anthropicPerClassNoOverallExceededHeaders models a genuine per-class
// sub-bucket limit: neither account-wide 5h nor 7d window is surpassed, but
// the upstream still 429'd (e.g. anthropic-ratelimit-unified-7d_opus-status
// rejected). calculateAnthropic429ResetTime yields result.window == "" here,
// which is the only case where model-scoping is correct.
func anthropicPerClassNoOverallExceededHeaders(resetAt int64) http.Header {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.30")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(resetAt, 10))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.40")
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(resetAt+3600*24, 10))
	return headers
}

func newG4RateLimitService(repo *rateLimitAccountRepoStub) *RateLimitService {
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAnthropicUpstreamErrorCounterCache(&anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1},
		tierCounts: []int64{0},
	})
	return svc
}

func TestG4_OverallWindow429_CoolsAccountNotModelClass(t *testing.T) {
	// An account-wide 5h window exhaustion blocks every model class (the shared
	// window is rejected regardless of per-class sub-bucket headroom — edge-us3
	// upstream probe). It MUST cool the whole account, not just opus.
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := &Account{ID: 901, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	resetAt := time.Now().Add(90 * time.Minute).Unix()
	shouldDisable := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		anthropic5hExceededHeaders(resetAt),
		[]byte(`{"error":{"type":"rate_limit_error","message":"unified 5h limit reached"}}`),
		"claude-opus-4-8",
	)

	// rate-limited (not error-disabled) account
	require.False(t, shouldDisable)
	// account-level rate limit IS written — the whole account is cooled
	require.Equal(t, 1, repo.setRateLimitedCalls,
		"account-wide 5h window exhaustion must call account-level SetRateLimited")
	// NO model-class cooldown — that would falsely leave sibling classes schedulable
	require.Empty(t, repo.modelRateLimitCalls,
		"account-wide window exhaustion must NOT be model-scoped")
	require.Equal(t, 0, repo.tempCalls,
		"authoritative SetRateLimited suppresses the 3/3 ladder temp-unschedulable")
	// account-global 5h session window recorded (operator usage gauge)
	require.Equal(t, 1, repo.updateSessionWindowCalls,
		"account-level path records the account-global 5h session window")
	require.Equal(t, "rejected", repo.lastSessionWindowStatus)
}

func TestG4_PerClassSubBucket429_ModelScopedSiblingsSchedulable(t *testing.T) {
	// Genuine per-class sub-bucket limit: neither overall 5h nor 7d window is
	// surpassed (result.window == ""), so model-scoping is correct — only the
	// requested class is cooled and the account-global window is NOT touched.
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := &Account{ID: 921, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	resetAt := time.Now().Add(90 * time.Minute).Unix()
	shouldDisable := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		anthropicPerClassNoOverallExceededHeaders(resetAt),
		[]byte(`{"error":{"type":"rate_limit_error","message":"per-class limit reached"}}`),
		"claude-opus-4-8",
	)

	require.False(t, shouldDisable)
	// model-class cooldown for opus written exactly once
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, int64(921), call.accountID)
	require.Equal(t, tkModelClassScope("opus"), call.scope)
	require.Equal(t, tkAnthropicModelCooldownReason, call.reason)
	// NO account-level rate limit (siblings keep their genuine capacity)
	require.Equal(t, 0, repo.setRateLimitedCalls,
		"per-class sub-bucket limit must NOT cool the whole account")
	// per-class path must NOT force the account-global 5h session window rejected
	require.Equal(t, 0, repo.updateSessionWindowCalls,
		"per-class cooldown must not corrupt the account-global 5h session window")
}

func TestG4_OpusCooled_SonnetHaikuStillSchedulable(t *testing.T) {
	// Reader-side: represents a genuine per-class cooldown (only opus class
	// cooled). Simulate the post-write account state: opus class cooled until reset.
	resetAt := time.Now().Add(90 * time.Minute)
	account := &Account{
		ID:          902,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				tkModelClassScope("opus"): map[string]any{
					"rate_limit_reset_at": resetAt.Format(time.RFC3339),
					"reason":              tkAnthropicModelCooldownReason,
				},
			},
		},
	}

	ctx := context.Background()
	// opus is NOT schedulable (its class is cooled)
	require.False(t, account.IsSchedulableForModelWithContext(ctx, "claude-opus-4-8"),
		"opus must be unschedulable while its class window is exhausted")
	// sonnet / haiku ARE still schedulable on the same account
	require.True(t, account.IsSchedulableForModelWithContext(ctx, "claude-sonnet-4-5"),
		"sonnet must stay schedulable when only opus class is cooled")
	require.True(t, account.IsSchedulableForModelWithContext(ctx, "claude-3-5-haiku-20241022"),
		"haiku must stay schedulable when only opus class is cooled")

	// remaining-time hint reflects the class cooldown for opus, zero for siblings
	require.InDelta(t, time.Until(resetAt).Seconds(),
		account.GetRateLimitRemainingTimeWithContext(ctx, "claude-opus-4-8").Seconds(), 2)
	require.Zero(t, account.GetRateLimitRemainingTimeWithContext(ctx, "claude-sonnet-4-5"))
}

func TestG4_ResetRecovery_OpusSchedulableAfterReset(t *testing.T) {
	// reset already in the past → cooldown expired → opus schedulable again.
	pastReset := time.Now().Add(-1 * time.Minute)
	account := &Account{
		ID:          903,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				tkModelClassScope("opus"): map[string]any{
					"rate_limit_reset_at": pastReset.Format(time.RFC3339),
				},
			},
		},
	}
	ctx := context.Background()
	require.True(t, account.IsSchedulableForModelWithContext(ctx, "claude-opus-4-8"),
		"opus must recover once the class cooldown reset time has passed")
	require.Zero(t, account.GetRateLimitRemainingTimeWithContext(ctx, "claude-opus-4-8"))
}

// --- account-level errors stay account-level (NOT model-scoped) ---------------

func TestG4_CreditBalance400_StaysAccountLevel(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := &Account{ID: 904, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	shouldDisable := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadRequest,
		http.Header{},
		[]byte(`{"error":{"type":"invalid_request_error","message":"Your credit balance is too low"}}`),
		"claude-opus-4-8",
	)

	require.True(t, shouldDisable, "credit balance exhaustion disables the whole account")
	require.Empty(t, repo.modelRateLimitCalls,
		"credit balance is an account-level failure, never model-scoped")
	require.Greater(t, repo.setErrorCalls, 0, "credit balance → SetError (account-level)")
}

func TestG4_OAuth401_StaysAccountLevel(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := &Account{
		ID:       905,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt-present",
		},
	}

	svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusUnauthorized,
		http.Header{},
		[]byte(`{"error":{"type":"authentication_error","message":"invalid token"}}`),
		"claude-opus-4-8",
	)

	require.Empty(t, repo.modelRateLimitCalls,
		"401 auth failure is account-level, never model-scoped")
	require.Equal(t, 1, repo.setErrorCalls, "Anthropic OAuth 401 → SetError (account-level)")
	require.Equal(t, 0, repo.tempCalls, "must not temp_unschedulable hold")
}

func TestG4_529Overloaded_StaysAccountLevel(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{
		RateLimit: config.RateLimitConfig{OverloadCooldownMinutes: 5},
	}, nil, nil)
	svc.SetAnthropicUpstreamErrorCounterCache(&anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1},
		tierCounts: []int64{0},
	})
	account := &Account{ID: 906, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	svc.HandleUpstreamError(
		context.Background(),
		account,
		529,
		http.Header{},
		[]byte(`{"error":{"type":"overloaded_error","message":"overloaded"}}`),
		"claude-opus-4-8",
	)

	require.Empty(t, repo.modelRateLimitCalls,
		"529 overloaded is account-wide capacity pressure, never model-scoped")
	require.Greater(t, repo.setOverloadedCalls, 0, "529 → SetOverloaded (account-level)")
}

// --- fallback: unknown model class → account-level ----------------------------

func TestG4_UnknownModelClass_FallsBackToAccountLevel(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := &Account{ID: 907, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	resetAt := time.Now().Add(90 * time.Minute).Unix()
	svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		anthropic5hExceededHeaders(resetAt),
		[]byte(`{"error":{"type":"rate_limit_error","message":"unified 5h limit reached"}}`),
		"some-non-anthropic-model",
	)

	require.Empty(t, repo.modelRateLimitCalls,
		"unknown model class cannot be safely narrowed → no model-scoped write")
	require.Equal(t, 1, repo.setRateLimitedCalls,
		"unknown model class falls back to account-level SetRateLimited so the window is still cooled")
}

func TestG4_NoModelContext_FallsBackToAccountLevel(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := &Account{ID: 908, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	resetAt := time.Now().Add(90 * time.Minute).Unix()
	// No requestedModel argument — e.g. a call site that doesn't thread the model.
	svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		anthropic5hExceededHeaders(resetAt),
		[]byte(`{"error":{"type":"rate_limit_error","message":"unified 5h limit reached"}}`),
	)

	require.Empty(t, repo.modelRateLimitCalls,
		"no model context → cannot scope → no model-scoped write")
	require.Equal(t, 1, repo.setRateLimitedCalls,
		"no model context falls back to account-level SetRateLimited")
}

// --- non-Anthropic platforms are untouched ------------------------------------

func TestG4_NonAnthropicPlatform_NoModelClassCooldown(t *testing.T) {
	account := &Account{ID: 909, Platform: PlatformOpenAI}
	require.False(t, account.tkAnthropicModelClassRateLimitActive("claude-opus-4-8"))
	require.Zero(t, account.tkAnthropicModelClassRateLimitRemaining("claude-opus-4-8"))
}

// ============================================================================
// OpenAI/Codex per-model metered sub-limit (spark) cooldown
// ============================================================================
//
// A codex usage_limit_reached 429 reaches handle429's body path (path 2) only
// when no account-wide window is >=100% (calculateOpenAI429ResetTime returned
// nil). There, a spark sub-limit 429 with a HEALTHY account-wide window is
// narrowed to (account × model) so the account keeps serving other models and
// spark fails over; an account-wide-near-cap window still cools the whole
// account.

// codexGeneralWindowHeaders models the account-wide codex window via the
// x-codex-primary(5h)/secondary(7d)-* headers. window-minutes pin the 5h/7d
// mapping the same way prod sends them (primary=300, secondary=10080).
func codexGeneralWindowHeaders(used5h, used7d int) http.Header {
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", strconv.Itoa(used5h))
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-secondary-used-percent", strconv.Itoa(used7d))
	h.Set("x-codex-secondary-window-minutes", "10080")
	return h
}

const codexSparkModel = "gpt-5.3-codex-spark"

// usage_limit_reached body carrying the (spark sub-window) reset.
var codexUsageLimitBody = []byte(`{"error":{"type":"usage_limit_reached","message":"limit reached","resets_in_seconds":7620}}`)

func newOpenAICodexAccount(id int64, accType string) *Account {
	return &Account{ID: id, Platform: PlatformOpenAI, Type: accType}
}

func TestCodexSpark429_GeneralWindowHealthy_ModelScoped(t *testing.T) {
	// Spark sub-limit hit while the account-wide window is healthy (5h=4%,7d=1%):
	// cool ONLY the spark model, leave the account schedulable for other models.
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := newOpenAICodexAccount(1001, AccountTypeOAuth)

	svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		codexGeneralWindowHeaders(4, 1),
		codexUsageLimitBody,
		codexSparkModel,
	)

	require.Len(t, repo.modelRateLimitCalls, 1,
		"healthy general window → spark 429 must be model-scoped")
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, int64(1001), call.accountID)
	require.Equal(t, codexSparkModel, call.scope,
		"scope is the mapped model so the scheduler's GetMappedModel key matches")
	require.Equal(t, tkOpenAICodexMeteredCooldownReason, call.reason)
	require.Equal(t, 0, repo.setRateLimitedCalls,
		"model-scoped spark cooldown must NOT cool the whole account")
}

func TestCodexSpark429_GeneralWindowNearCapNotExhausted_ModelScoped(t *testing.T) {
	// Account-wide 7d window near its cap (97%) but NOT exhausted (<100%): the
	// binding limit on this spark request is still the spark sub-window, so it is
	// model-scoped. We deliberately do NOT whole-account cool on an INFERRED
	// sub-100% threshold — only the fact-based >=100% (handled by path 1) cools
	// the whole account. The account keeps its remaining general headroom; if 7d
	// truly hits 100%, the next request is routed to whole-account by path 1.
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := newOpenAICodexAccount(1002, AccountTypeOAuth)

	svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		codexGeneralWindowHeaders(5, 97),
		codexUsageLimitBody,
		codexSparkModel,
	)

	require.Len(t, repo.modelRateLimitCalls, 1,
		"a sub-100% account-wide window still model-scopes the spark sub-limit (no inferred threshold)")
	require.Equal(t, codexSparkModel, repo.modelRateLimitCalls[0].scope)
	require.Equal(t, 0, repo.setRateLimitedCalls)
}

func TestCodexSpark429_GeneralWindowExhausted_WholeAccountViaPath1(t *testing.T) {
	// When a general window actually reads >=100% AND carries its reset, path 1
	// (calculateOpenAI429ResetTime) cools the WHOLE account before the model-scope
	// helper is reached — this is the only fact-based account-wide signal. Also
	// the header-semantics self-protection: if the x-codex-* headers ever
	// reflected the spark window at 100% instead of the account-wide one, this
	// same path catches it. No model-scoped write.
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := newOpenAICodexAccount(1003, AccountTypeOAuth)

	headers := codexGeneralWindowHeaders(100, 1) // 5h reads 100%
	headers.Set("x-codex-primary-reset-after-seconds", "7620")

	svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		headers,
		codexUsageLimitBody,
		codexSparkModel,
	)

	require.Empty(t, repo.modelRateLimitCalls,
		"a >=100% account-wide window is whole-account cooled by path 1, never model-scoped")
	require.Equal(t, 1, repo.setRateLimitedCalls)
}

func TestCodex429_NonSparkModel_GeneralHealthy_WholeAccount(t *testing.T) {
	// A non-metered model (gpt-5.4) draws on the account-wide window; a 429 there
	// is not a per-model sub-limit, so it is never model-scoped.
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := newOpenAICodexAccount(1004, AccountTypeOAuth)

	svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		codexGeneralWindowHeaders(4, 1),
		codexUsageLimitBody,
		"gpt-5.4",
	)

	require.Empty(t, repo.modelRateLimitCalls,
		"non-spark model must not be model-scoped")
	require.Equal(t, 1, repo.setRateLimitedCalls)
}

func TestCodexSpark429_MirrorApikeyAccount_WholeAccount(t *testing.T) {
	// prod→edge mirror accounts are type=apikey (the real spark window lives at
	// the edge). They must keep whole-account behaviour.
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := newOpenAICodexAccount(1005, AccountTypeAPIKey)

	svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		codexGeneralWindowHeaders(4, 1),
		codexUsageLimitBody,
		codexSparkModel,
	)

	require.Empty(t, repo.modelRateLimitCalls,
		"mirror apikey account must not be model-scoped")
	require.Equal(t, 1, repo.setRateLimitedCalls)
}

func TestCodexSpark429_NoModelContext_WholeAccount(t *testing.T) {
	// WS fast-path drops the model. Without it we cannot scope → whole account.
	repo := &rateLimitAccountRepoStub{}
	svc := newG4RateLimitService(repo)
	account := newOpenAICodexAccount(1006, AccountTypeOAuth)

	svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		codexGeneralWindowHeaders(4, 1),
		codexUsageLimitBody,
	)

	require.Empty(t, repo.modelRateLimitCalls)
	require.Equal(t, 1, repo.setRateLimitedCalls)
}

// --- reader side: a spark-scoped cooldown keeps other models schedulable ------

func TestCodexSpark_ModelScoped_OtherModelsStaySchedulable(t *testing.T) {
	resetAt := time.Now().Add(90 * time.Minute)
	account := &Account{
		ID:          1007,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				codexSparkModel: map[string]any{
					"rate_limit_reset_at": resetAt.Format(time.RFC3339),
					"reason":              tkOpenAICodexMeteredCooldownReason,
				},
			},
		},
	}
	ctx := context.Background()
	require.False(t, account.IsSchedulableForModelWithContext(ctx, codexSparkModel),
		"spark is unschedulable while its model-scoped cooldown is active")
	require.True(t, account.IsSchedulableForModelWithContext(ctx, "gpt-5.4"),
		"non-spark models stay schedulable on the same account")
	require.InDelta(t, time.Until(resetAt).Seconds(),
		account.GetModelRateLimitRemainingTimeWithContext(ctx, codexSparkModel).Seconds(), 2)
	require.Zero(t, account.GetModelRateLimitRemainingTimeWithContext(ctx, "gpt-5.4"))
}

// --- pure predicate boundaries ------------------------------------------------

func TestTkIsOpenAICodexMeteredModel(t *testing.T) {
	for _, m := range []string{"gpt-5.3-codex-spark", "GPT-5.3-CODEX-SPARK", "  gpt-5.3-codex-spark  "} {
		require.Truef(t, tkIsOpenAICodexMeteredModel(m), "model=%q should be metered", m)
	}
	for _, m := range []string{"gpt-5.3-codex", "gpt-5.4", "codex", "spark", "", "claude-opus-4-8"} {
		require.Falsef(t, tkIsOpenAICodexMeteredModel(m), "model=%q should NOT be metered", m)
	}
}
