//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTkIsKiroEndpointQuotaExhausted(t *testing.T) {
	require.Equal(t, 10, KiroEndpointQuotaExhaustedRetryAfterSeconds())
	require.True(t, tkIsKiroEndpointQuotaExhausted("quota exhausted on AmazonQ", nil))
	require.True(t, tkIsKiroEndpointQuotaExhausted("", []byte(`quota exhausted on Kiro Runtime`)))
	require.True(t, tkIsKiroEndpointQuotaExhausted(tkKiroEndpointQuotaExhaustedClient, nil))
	require.False(t, tkIsKiroEndpointQuotaExhausted("Upstream request failed", nil))
	require.False(t, tkIsKiroEndpointQuotaExhausted("", []byte(`{"error":{"message":"slow down"}}`)))
}

func TestHandleUpstreamError_KiroEndpointQuotaExhausted_Uses10sCooldownNotLadder(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5}}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAnthropicUpstreamErrorCounterCache(counter)

	account := newEdgeUS4KiroAccount()
	body := []byte("quota exhausted on AmazonQ")
	for i := 0; i < 5; i++ {
		shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, nil, body)
		require.True(t, shouldDisable, "iteration %d", i)
	}

	require.Equal(t, 5, repo.tempCalls, "each quota-exhausted hit must write a short cooldown")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "must not advance anthropic_upstream_error 3/3 ladder")
	require.Contains(t, repo.lastTempReason, tkKiroEndpointQuotaExhaustedReason)
}

func TestHandleUpstreamError_KiroModelTemporarilyRestricted_CoolsOnlyRequestedModel(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	account := newEdgeUS4KiroAccount()
	body := []byte("kiro model claude-opus-5-5 temporarily restricted: Your access to this model has been temporarily restricted due to excessive content refusals.")
	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, nil, body, "claude-opus-5-5")
	require.False(t, shouldDisable, "model-scoped restriction must keep the account active for other models")
	require.Equal(t, 0, repo.tempCalls)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, account.ID, repo.modelRateLimitCalls[0].accountID)
	require.Equal(t, "claude-opus-5-5", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, kiroModelTemporarilyRestrictedReason, repo.modelRateLimitCalls[0].reason)
}
