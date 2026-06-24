//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTkIsDownstreamNoAvailableAccounts(t *testing.T) {
	// Edge mirror-stub 503 body (our own gateway error envelope).
	require.True(t, tkIsDownstreamNoAvailableAccounts("", []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)))
	// Already-parsed upstream message.
	require.True(t, tkIsDownstreamNoAvailableAccounts("No available accounts: no available accounts", nil))
	// Case-insensitive.
	require.True(t, tkIsDownstreamNoAvailableAccounts("NO AVAILABLE ACCOUNTS", nil))
	// A genuine Anthropic upstream error must not match.
	require.False(t, tkIsDownstreamNoAvailableAccounts("Internal server error", []byte(`{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`)))
	require.False(t, tkIsDownstreamNoAvailableAccounts("", []byte(`{}`)))
}

func TestTkSkipDownstreamNoAvailableAccountsPenalty(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	require.True(t, tkSkipDownstreamNoAvailableAccountsPenalty(http.StatusServiceUnavailable, "", body))
	require.True(t, tkSkipDownstreamNoAvailableAccountsPenalty(http.StatusTooManyRequests, "", body))
	require.False(t, tkSkipDownstreamNoAvailableAccountsPenalty(http.StatusBadGateway, "", body))
	require.False(t, tkSkipDownstreamNoAvailableAccountsPenalty(http.StatusTooManyRequests, "", []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)))
}

func TestTkSkipDownstreamKiroOAuthAuthRejectPenalty(t *testing.T) {
	stub := &Account{
		ID:       66,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": "kiro",
		},
	}
	body := []byte(`{"error":{"message":"Upstream request failed","type":"upstream_error"},"type":"error"}`)

	require.True(t, tkSkipDownstreamKiroOAuthAuthRejectPenalty(stub, http.StatusUnauthorized, "", body))
	require.True(t, tkSkipDownstreamKiroOAuthAuthRejectPenalty(stub, http.StatusUnauthorized, "Invalid bearer token", nil))
	require.False(t, tkSkipDownstreamKiroOAuthAuthRejectPenalty(stub, http.StatusForbidden, "", body))
	require.True(t, tkSkipDownstreamKiroOAuthAuthRejectPenalty(stub, http.StatusForbidden, "Invalid bearer token", nil))
	require.False(t, tkSkipDownstreamKiroOAuthAuthRejectPenalty(stub, http.StatusBadRequest, "Invalid bearer token", nil))

	plainAnthropic := &Account{ID: 67, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	require.False(t, tkSkipDownstreamKiroOAuthAuthRejectPenalty(plainAnthropic, http.StatusUnauthorized, "Invalid bearer token", nil))
	require.False(t, tkSkipDownstreamKiroOAuthAuthRejectPenalty(plainAnthropic, http.StatusForbidden, "Invalid bearer token", nil))

	openaiMirror := &Account{
		ID:       68,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": "openai",
		},
	}
	require.False(t, tkSkipDownstreamKiroOAuthAuthRejectPenalty(openaiMirror, http.StatusUnauthorized, "Invalid bearer token", nil))
	require.False(t, tkSkipDownstreamKiroOAuthAuthRejectPenalty(openaiMirror, http.StatusForbidden, "Invalid bearer token", nil))
}

// prod incident 2026-05-31: a downstream edge stub returning 503 "no available
// accounts" (a transient edge pool-capacity blip) must fail the request over to
// the next stub WITHOUT advancing the per-account cooldown counter or cooling the
// stub — otherwise a 3-503 edge burst trips the 3/3 ladder and blacks out the
// whole edge stub for 10 minutes, collapsing the prod pool.
func TestRateLimitService_HandleUpstreamError_DownstreamNoAvailable_DoesNotPenalizeStub(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 4042, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	for i := 0; i < 5; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body)
		require.True(t, shouldDisable, "iteration %d: downstream 503 no-available must fail over to the next stub", i)
	}

	require.Equal(t, 0, repo.tempCalls, "downstream no-available 503 must never write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "downstream no-available 503 must NOT advance the cooldown counter")
}

// prod 2026-06: empty-pool fast-fail now returns 429+Retry-After (not 503). The
// prod mirror stub must fail over without handle429 cooldown or Feishu "限流冷却".
func TestRateLimitService_HandleUpstreamError_DownstreamNoAvailable429_DoesNotPenalizeStub(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 4042, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	headers := http.Header{}
	headers.Set("Retry-After", "5")
	for i := 0; i < 5; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, body)
		require.True(t, shouldDisable, "iteration %d: downstream 429 no-available must fail over to the next stub", i)
	}

	require.Equal(t, 0, repo.setRateLimitedCalls, "downstream no-available 429 must not write SetRateLimited")
	require.Equal(t, 0, repo.tempCalls, "downstream no-available 429 must never write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "downstream no-available 429 must NOT advance the cooldown counter")
}

// fakeSaturationCounterRL records IncrementSaturation calls for the increment-
// side test (read methods are unused on the rate-limit side).
type fakeSaturationCounterRL struct{ incrementIDs []int64 }

func (f *fakeSaturationCounterRL) IncrementSaturation(_ context.Context, accountID int64, _ int) (int64, error) {
	f.incrementIDs = append(f.incrementIDs, accountID)
	return int64(len(f.incrementIDs)), nil
}
func (f *fakeSaturationCounterRL) GetSaturationBatch(_ context.Context, _ []int64) (map[int64]int64, error) {
	return map[int64]int64{}, nil
}

// The saturation feature MUST feed the de-prioritization counter on the skip
// path while NEVER advancing the 3/3 ladder or writing temp_unschedulable /
// SetRateLimited. This is the load-bearing invariant: penalty, not cooldown.
func TestRateLimitService_DownstreamNoAvailable_FeedsSaturationButNotLadder(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	ladder := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5, 6}}
	sat := &fakeSaturationCounterRL{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(ladder)
	service.SetAnthropicSaturationCounter(sat)
	account := &Account{ID: 4044, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	// 429 empty-pool fast-fail.
	noAvail := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	for i := 0; i < 3; i++ {
		require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, noAvail))
	}
	// 502 failover-exhausted (sibling capacity signal).
	exhausted := []byte(`{"type":"error","error":{"type":"server_error","message":"all available accounts exhausted"}}`)
	for i := 0; i < 2; i++ {
		require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, exhausted))
	}

	require.Equal(t, []int64{4044, 4044, 4044, 4044, 4044}, sat.incrementIDs,
		"every downstream-capacity hit must feed the saturation preference counter")
	require.Empty(t, ladder.incrementIDs, "saturation feature must NOT advance the 3/3 ladder")
	require.Equal(t, 0, repo.tempCalls, "saturation feature must NOT write temp_unschedulable")
	require.Equal(t, 0, repo.setRateLimitedCalls, "saturation feature must NOT write SetRateLimited")
	require.Equal(t, 0, repo.setErrorCalls)
}

// When the saturation counter is unwired (nil), the skip path behaves exactly as
// before — proving the increment is an optional, inert-by-default dependency.
func TestRateLimitService_DownstreamNoAvailable_NilSaturationCounterIsInert(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 4045, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	noAvail := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, noAvail))
	require.Equal(t, 0, repo.tempCalls)
}

func TestRateLimitService_HandleUpstreamError_KiroMirrorOAuth401_DoesNotSetError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	sat := &fakeSaturationCounterRL{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicSaturationCounter(sat)
	account := &Account{
		ID:       66,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": "kiro",
			"pool_mode":       true,
		},
	}
	body := []byte(`{"error":{"message":"Upstream request failed","type":"upstream_error"},"type":"error"}`)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, body)

	require.True(t, shouldDisable, "in-flight request must fail over / let pool-mode retry, but stub health stays untouched")
	require.Equal(t, 0, repo.setErrorCalls, "downstream Kiro OAuth 401 must not permanently disable the prod relay stub")
	require.Equal(t, 0, repo.tempCalls, "downstream Kiro OAuth 401 must not ladder-cool the prod relay stub")
	require.Equal(t, 0, repo.setRateLimitedCalls)
	require.Equal(t, []int64{66}, sat.incrementIDs, "transient downstream auth blip should feed bounded de-prioritization")
}

func TestRateLimitService_HandleUpstreamError_KiroMirrorOAuth403_DoesNotSetError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	ladder := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}}
	sat := &fakeSaturationCounterRL{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(ladder)
	service.SetAnthropicSaturationCounter(sat)
	account := &Account{
		ID:       66,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": "kiro",
			"pool_mode":       true,
		},
	}
	body := []byte(`{"error":{"message":"Invalid bearer token","type":"upstream_error"},"type":"error"}`)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	require.True(t, shouldDisable, "in-flight request must fail over / let pool-mode retry, but stub health stays untouched")
	require.Equal(t, 0, repo.setErrorCalls, "downstream Kiro OAuth 403 must not permanently disable the prod relay stub")
	require.Equal(t, 0, repo.tempCalls, "downstream Kiro OAuth 403 must not ladder-cool the prod relay stub")
	require.Equal(t, 0, repo.setRateLimitedCalls)
	require.Empty(t, ladder.incrementIDs, "downstream Kiro OAuth 403 must not advance the 3/3 ladder")
	require.Equal(t, []int64{66}, sat.incrementIDs, "transient downstream auth blip should feed bounded de-prioritization")
}

func TestRateLimitService_HandleUpstreamError_KiroMirrorGeneric403_StillCounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{3}, tierCounts: []int64{1}}
	sat := &fakeSaturationCounterRL{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	service.SetAnthropicSaturationCounter(sat)
	account := &Account{
		ID:       66,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": "kiro",
			"pool_mode":       true,
		},
	}
	body := []byte(`{"error":{"message":"Upstream request failed","type":"upstream_error"},"type":"error"}`)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "generic Kiro mirror 403 must not use the invalid-bearer skip")
	require.Equal(t, 1, repo.tempCalls, "generic Kiro mirror 403 must still enter the upstream-error ladder")
	require.Equal(t, []int64{66}, counter.incrementIDs)
	require.Empty(t, sat.incrementIDs, "generic Kiro mirror 403 must not feed auth-reject saturation")
}

func TestRateLimitService_HandleUpstreamError_PlainAnthropicAPIKey401_StillSetsError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 67, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"invalid x-api-key","type":"authentication_error"},"type":"error"}`)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "real Anthropic api-key 401 must keep the permanent-disable guard")
	require.Contains(t, repo.lastErrorMsg, "Authentication failed (401)")
}

func TestRateLimitService_HandleUpstreamError_PlainAnthropicAPIKey403_StillCounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{3}, tierCounts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 67, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"Invalid bearer token","type":"permission_error"},"type":"error"}`)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls, "generic Anthropic API-key 403 must not use the Kiro mirror skip")
	require.Equal(t, 1, repo.tempCalls, "generic Anthropic API-key 403 must still enter the upstream-error ladder")
	require.Equal(t, []int64{67}, counter.incrementIDs)
}

// Regression: a genuine Anthropic upstream 503 (no "no available accounts" body)
// still flows through the threshold path so persistent real upstream failure
// continues to escalate and eventually cools the account.
func TestRateLimitService_HandleUpstreamError_GenuineUpstream503_StillCounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}, tierCounts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 4043, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := []byte(`{"type":"error","error":{"type":"api_error","message":"upstream request failed"}}`)
	for i := 0; i < 2; i++ {
		require.False(t, service.HandleUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body))
	}
	require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body),
		"3rd genuine upstream 503 must trip the threshold")
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, []int64{4043, 4043, 4043}, counter.incrementIDs)
}
