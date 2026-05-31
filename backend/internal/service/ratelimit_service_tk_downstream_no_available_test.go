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
