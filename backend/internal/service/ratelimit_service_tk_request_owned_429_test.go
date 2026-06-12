//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Incident 2026-06-11: a deterministic policy 429 ("Usage credits are required
// for long context requests.") fanned out by failover must never advance the
// anthropic 3/3 ladder or write any cooldown — repeated arrivals poisoned all 7
// mirror accounts into tier-2 10-minute cooldowns. Defense-in-depth mirror of
// the gateway-level short circuit, for every other HandleUpstreamError caller.
func TestRateLimitService_HandleUpstreamError_RequestOwned429_DoesNotPenalize(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 4043, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests."}}`)
	for i := 0; i < 5; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)
		require.True(t, shouldDisable, "iteration %d: request-owned 429 must fail this request away from the account", i)
	}

	require.Equal(t, 0, repo.setRateLimitedCalls, "request-owned 429 must not write SetRateLimited")
	require.Equal(t, 0, repo.tempCalls, "request-owned 429 must never write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "request-owned 429 must NOT advance the 3/3 ladder counter")
}

// A genuine anthropic rate-limit 429 must keep the pre-existing penalty path
// (regression guard for the new skip being too broad).
func TestRateLimitService_HandleUpstreamError_Genuine429_StillPenalizes(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 4044, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`)
	service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)

	require.NotEmpty(t, counter.incrementIDs, "genuine 429 must still advance the ladder counter")
}
