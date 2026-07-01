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
		shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, nil, body)
		require.True(t, shouldDisable, "iteration %d", i)
	}

	require.Equal(t, 5, repo.tempCalls, "each quota-exhausted hit must write a short cooldown")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "must not advance anthropic_upstream_error 3/3 ladder")
	require.Contains(t, repo.lastTempReason, tkKiroEndpointQuotaExhaustedReason)
}

func TestHandleUpstreamError_KiroMirrorStubQuotaExhausted429_SkipsAnthropicLadder(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAnthropicUpstreamErrorCounterCache(counter)

	stub := &Account{
		ID:       71,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": "kiro",
		},
	}
	msg := tkKiroEndpointQuotaExhaustedClient
	for i := 0; i < 3; i++ {
		shouldDisable := svc.HandleUpstreamError(context.Background(), stub, http.StatusTooManyRequests, nil, []byte(msg))
		require.True(t, shouldDisable, "iteration %d", i)
	}

	require.Equal(t, 3, repo.tempCalls)
	require.Empty(t, counter.incrementIDs, "kiro mirror stub must not use anthropic 3/3 ladder on quota exhaustion")
}
