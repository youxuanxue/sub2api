//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTkIsAnthropicClientInducedBadRequest(t *testing.T) {
	require.True(t, tkIsAnthropicClientInducedBadRequest([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max 4 cache_control blocks, found 5"}}`)))
	require.True(t, tkIsAnthropicClientInducedBadRequest([]byte(`{"error":{"type":"INVALID_REQUEST_ERROR"}}`)))
	require.False(t, tkIsAnthropicClientInducedBadRequest([]byte(`{"error":{"type":"api_error"}}`)))
	require.False(t, tkIsAnthropicClientInducedBadRequest([]byte(`{}`)))
}

// upstream Wei-Shaw/sub2api#2608: repeated client-induced 400 invalid_request_error
// responses must NOT advance the per-account cooldown counter or pause the
// account — otherwise any caller can disable a shared OAuth subscription account
// by spamming malformed requests.
func TestRateLimitService_HandleUpstreamError_Anthropic400ClientInduced_DoesNotPenalizeAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 2608, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	body := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`)
	for i := 0; i < 5; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, body)
		require.False(t, shouldDisable, "iteration %d: client-induced 400 must not disable the account", i)
	}

	require.Equal(t, 0, repo.tempCalls, "#2608: client-induced 400 must never write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "#2608: client-induced 400 must NOT advance the cooldown counter")
}

// Regression: an atypical (non invalid_request_error) Anthropic 400 still goes
// through the normal threshold path so genuinely account-affecting 400s are not
// silently ignored.
func TestRateLimitService_HandleUpstreamError_Anthropic400NonClientInduced_StillCounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}, tierCounts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 2609, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	body := []byte(`{"type":"error","error":{"type":"api_error","message":"internal anomaly"}}`)
	for i := 0; i < 2; i++ {
		require.False(t, service.HandleUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, body))
	}
	require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, body),
		"3rd non-client-induced 400 must trip the threshold")
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, []int64{2609, 2609, 2609}, counter.incrementIDs)
}
