//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const antigravityInternal500Body = `{"error":{"code":500,"message":"Internal error encountered.","status":"INTERNAL"}}`

// internal500ScopeRepo records every account-state write the INTERNAL 500 path can make.
type internal500ScopeRepo struct {
	AccountRepository
	modelCalls  []modelRateLimitCall
	reasons     []string
	tempUnsched int
	setErrors   int
}

func (r *internal500ScopeRepo) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	r.modelCalls = append(r.modelCalls, modelRateLimitCall{accountID: id, modelKey: scope, resetAt: resetAt})
	if len(reason) > 0 {
		r.reasons = append(r.reasons, reason[0])
	}
	return nil
}

func (r *internal500ScopeRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	r.tempUnsched++
	return nil
}

func (r *internal500ScopeRepo) SetError(context.Context, int64, string) error {
	r.setErrors++
	return nil
}

func newInternal500Upstream(n int) *mockSmartRetryUpstream {
	up := &mockSmartRetryUpstream{}
	for i := 0; i < n; i++ {
		up.responses = append(up.responses, &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewReader([]byte(antigravityInternal500Body))),
		})
		up.errors = append(up.errors, nil)
	}
	return up
}

func antigravityTestAccount(id int64, plan string) *Account {
	creds := map[string]any{
		"model_mapping": map[string]any{
			"gemini-3.8-flash":    "gemini-3.8-flash",
			"gemini-4-argon-high": "gemini-4-argon-high",
		},
	}
	if plan != "" {
		creds["plan_type"] = plan
	}
	return &Account{
		ID: id, Name: "acc", Type: AccountTypeOAuth, Platform: PlatformAntigravity,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: creds,
	}
}

// #8521: an INTERNAL 500 that survives every retry must cool only the failing
// model. Before the fix this ran handleInternal500RetryExhausted and set the
// whole account temp-unschedulable for 30 minutes.
func TestAntigravityRetryLoop_Internal500_CoolsOnlyFailingModel(t *testing.T) {
	repo := &internal500ScopeRepo{}
	cache := &mockInternal500Cache{incrementCount: 1}
	account := antigravityTestAccount(901, "")
	svc := &AntigravityGatewayService{
		accountRepo:      repo,
		internal500Cache: cache,
		retryWait:        immediateAntigravityRetryWait,
		retryBackoff:     func(context.Context, int) bool { return true },
	}

	result, err := svc.antigravityRetryLoop(antigravityRetryLoopParams{
		ctx: context.Background(), prefix: "[test]", account: account,
		accessToken: "token", action: "generateContent", body: []byte(`{}`),
		httpUpstream: newInternal500Upstream(antigravityMaxRetries), accountRepo: repo,
		requestedModel: "gemini-3.8-flash",
		handleError: func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult {
			return nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, result.resp.StatusCode)

	require.Zero(t, repo.tempUnsched, "account must stay schedulable")
	require.Zero(t, repo.setErrors, "account must not be disabled")
	require.Empty(t, cache.incrementCalls, "account INTERNAL 500 ladder must not advance")
	require.Len(t, repo.modelCalls, 1)
	require.Equal(t, "gemini-3.8-flash", repo.modelCalls[0].modelKey, "exact model only, never the family key")
	require.Equal(t, []string{antigravityInternal500ModelCooldownReason}, repo.reasons)
	require.WithinDuration(t, time.Now().Add(antigravityInternal500ModelCooldown), repo.modelCalls[0].resetAt, 5*time.Second)

	// The account still serves other models.
	account.Extra = map[string]any{modelRateLimitsKey: map[string]any{
		"gemini-3.8-flash": map[string]any{"rate_limit_reset_at": repo.modelCalls[0].resetAt.UTC().Format(time.RFC3339)},
	}}
	require.False(t, account.IsSchedulableForModel("gemini-3.8-flash"))
	require.True(t, account.IsSchedulableForModel("gemini-4-argon-high"))
}

func TestHandleInternal500ForModel_UnresolvedModelFallsBackToAccountLadder(t *testing.T) {
	repo := &internal500ScopeRepo{}
	cache := &mockInternal500Cache{incrementCount: 1}
	svc := &AntigravityGatewayService{accountRepo: repo, internal500Cache: cache}

	svc.handleInternal500ForModel(context.Background(), "[test]", antigravityTestAccount(903, ""), "  ")

	require.Empty(t, repo.modelCalls)
	require.Equal(t, []int64{903}, cache.incrementCalls)
	require.Equal(t, 1, repo.tempUnsched)
}

// #2302: Google retryDelay must be capped before it becomes a cooldown.
func TestParseAntigravitySmartRetryInfo_ClampsRetryDelay(t *testing.T) {
	body := func(delay string) []byte {
		return []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[` +
			`{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED","metadata":{"model":"gemini-4-argon-high"}},` +
			`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"` + delay + `"}]}}`)
	}
	require.Equal(t, 2*time.Hour, antigravityMaxModelCooldown)

	long := parseAntigravitySmartRetryInfo(body("568800s")) // "Resets in 158h"
	require.NotNil(t, long)
	require.Equal(t, antigravityMaxModelCooldown, long.RetryDelay)

	exact := parseAntigravitySmartRetryInfo(body("7200s"))
	require.Equal(t, antigravityMaxModelCooldown, exact.RetryDelay)

	short := parseAntigravitySmartRetryInfo(body("39s"))
	require.Equal(t, 39*time.Second, short.RetryDelay, "values below the cap pass through")
}

func TestHandleSmartRetry_LongRetryDelay_ModelCooldownIsCapped(t *testing.T) {
	respBody := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[` +
		`{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED","metadata":{"model":"gemini-3.8-flash"}},` +
		`{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"86400s"}]}}`)
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(respBody))}
	repo := &stubAntigravityAccountRepo{}
	svc := &AntigravityGatewayService{}

	result := svc.handleSmartRetry(antigravityRetryLoopParams{
		ctx: context.Background(), prefix: "[test]", account: antigravityTestAccount(904, ""), accountRepo: repo,
		handleError: func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult {
			return nil
		},
	}, resp, respBody, "https://example.invalid", 0, []string{"https://example.invalid"})

	require.NotNil(t, result.switchError)
	require.NotEmpty(t, repo.modelRateLimitCalls)
	for _, call := range repo.modelRateLimitCalls {
		require.WithinDuration(t, time.Now().Add(antigravityMaxModelCooldown), call.resetAt, 5*time.Second, "scope %s", call.modelKey)
	}
}

func TestAntigravityResolveResetTime_ClampsFarFutureReset(t *testing.T) {
	svc := &AntigravityGatewayService{}
	now := time.Now()

	far := now.Add(7 * 24 * time.Hour).Unix()
	require.WithinDuration(t, now.Add(antigravityMaxModelCooldown), svc.resolveResetTime(&far, time.Minute), 2*time.Second)

	near := now.Add(10 * time.Minute).Unix()
	require.Equal(t, time.Unix(near, 0), svc.resolveResetTime(&near, time.Minute))

	require.WithinDuration(t, now.Add(time.Minute), svc.resolveResetTime(nil, time.Minute), 2*time.Second)
}

// #8393: on a Pro account Argon has its own quota. An Argon cooldown must
// not block Flash. Before the fix both were written to antigravity:gemini.
func TestAntigravityArgonCooldown_ProAccountKeepsFlashSchedulable(t *testing.T) {
	repo := &stubAntigravityAccountRepo{}
	account := antigravityTestAccount(905, "Pro")
	svc := &AntigravityGatewayService{}
	resetAt := time.Now().Add(30 * time.Minute)

	require.True(t, svc.setAntigravityModelRateLimits(context.Background(), repo, account, "gemini-4-argon-high", "[test]", 429, resetAt, false))

	written := map[string]any{}
	for _, call := range repo.modelRateLimitCalls {
		written[call.modelKey] = map[string]any{"rate_limit_reset_at": call.resetAt.UTC().Format(time.RFC3339)}
	}
	require.Contains(t, written, "gemini-4-argon-high")
	require.Contains(t, written, antigravityGeminiArgonRateLimitKey)
	require.NotContains(t, written, antigravityGeminiModelRateLimitKey)

	account.Extra = map[string]any{modelRateLimitsKey: written}
	require.False(t, account.IsSchedulableForModel("gemini-4-argon-high"))
	require.False(t, account.IsSchedulableForModel("gemini-4-argon-low"), "Argon variants share the sub-family")
	require.True(t, account.IsSchedulableForModel("gemini-3.8-flash"), "Flash must stay schedulable")
}

func TestAntigravityArgonCooldown_NonProTiersKeepSharedPool(t *testing.T) {
	for _, plan := range []string{"Ultra", "g1-ultra-tier", "Free", ""} {
		t.Run("plan="+plan, func(t *testing.T) {
			repo := &stubAntigravityAccountRepo{}
			account := antigravityTestAccount(906, plan)
			svc := &AntigravityGatewayService{}
			resetAt := time.Now().Add(30 * time.Minute)

			require.True(t, svc.setAntigravityModelRateLimits(context.Background(), repo, account, "gemini-4-argon-high", "[test]", 429, resetAt, false))

			written := map[string]any{}
			for _, call := range repo.modelRateLimitCalls {
				written[call.modelKey] = map[string]any{"rate_limit_reset_at": call.resetAt.UTC().Format(time.RFC3339)}
			}
			require.Contains(t, written, antigravityGeminiModelRateLimitKey)
			account.Extra = map[string]any{modelRateLimitsKey: written}
			require.False(t, account.IsSchedulableForModel("gemini-3.8-flash"), "shared pool still cools Flash")
		})
	}
}

func TestAntigravityArgonCooldown_ProFlashCooldownDoesNotBlockArgon(t *testing.T) {
	account := antigravityTestAccount(907, "g1-pro-tier")
	resetAt := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
	account.Extra = map[string]any{modelRateLimitsKey: map[string]any{
		"gemini-3.8-flash":                 map[string]any{"rate_limit_reset_at": resetAt},
		antigravityGeminiModelRateLimitKey: map[string]any{"rate_limit_reset_at": resetAt},
	}}
	require.False(t, account.IsSchedulableForModel("gemini-3.8-flash"))
	require.True(t, account.IsSchedulableForModel("gemini-4-argon-high"))
}

// A sub-family cooldown written while the account was Pro must still block the
// shared pool after the account's tier changes, until it expires.
func TestAntigravityArgonCooldown_SubFamilySurvivesTierChange(t *testing.T) {
	account := antigravityTestAccount(908, "Ultra")
	account.Extra = map[string]any{modelRateLimitsKey: map[string]any{
		antigravityGeminiArgonRateLimitKey: map[string]any{"rate_limit_reset_at": time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)},
	}}
	require.False(t, account.IsSchedulableForModel("gemini-4-argon-high"))
	require.False(t, account.IsSchedulableForModel("gemini-3.8-flash"))
}
