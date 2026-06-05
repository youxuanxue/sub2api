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

// G4 — model-dimension cooldown for Anthropic unified-window 429s.
//
// Prod motivation (edge us6, account edge-ls-oh-3-d): opus burned the shared
// 5h window, upstream returned a unified-window 429, and the legacy
// account-level SetRateLimited pulled the WHOLE account out of scheduling —
// taking healthy sonnet/haiku offline with it. G4 scopes that cooldown to
// (account × model class) so only opus is cooled.

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
	require.Empty(t, tkAnthropicModelClassScopeKeyForModel("gpt-5.4"))
	require.Empty(t, tkAnthropicModelClassScopeKeyForModel(""))
}

// --- write side: 5h exceeded → model-class cooldown, NOT account-level --------

func anthropic5hExceededHeaders(resetAt int64) http.Header {
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.02")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(resetAt, 10))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.30")
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

func TestG4_OpusUnifiedWindow429_CoolsModelClassNotAccount(t *testing.T) {
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

	// model-scoped cooldown is authoritative → account is not error-disabled
	require.False(t, shouldDisable)
	// account-level rate limit MUST NOT be written (that's the amplification G4 removes)
	require.Equal(t, 0, repo.setRateLimitedCalls,
		"unified-window opus 429 must NOT call account-level SetRateLimited")
	require.Equal(t, 0, repo.tempCalls,
		"authoritative model cooldown suppresses the 3/3 ladder temp-unschedulable")
	// model-class cooldown for opus written exactly once
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, int64(901), call.accountID)
	require.Equal(t, tkModelClassScope("opus"), call.scope)
	require.Equal(t, tkAnthropicModelCooldownReason, call.reason)
	require.WithinDuration(t, time.Unix(resetAt, 0), call.resetAt, 2*time.Second)
	// account-global 5h window is still recorded (operator usage gauge depends
	// on it) — only the cooldown SCOPE narrowed, not the window signal.
	require.Equal(t, 1, repo.updateSessionWindowCalls,
		"model-scoped cooldown must still record the account-global 5h session window")
	require.Equal(t, "rejected", repo.lastSessionWindowStatus)
}

func TestG4_OpusCooled_SonnetHaikuStillSchedulable(t *testing.T) {
	// Simulate the post-write account state: opus class cooled until reset.
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
		"401 auth failure is account-level (temp-unschedulable for token refresh), never model-scoped")
	require.Greater(t, repo.tempCalls, 0, "OAuth 401 → SetTempUnschedulable (account-level)")
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
