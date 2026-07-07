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

func TestTkAnthropicStubHealthFuseEligible(t *testing.T) {
	require.True(t, tkAnthropicStubHealthFuseEligible(http.StatusBadGateway, false))
	require.True(t, tkAnthropicStubHealthFuseEligible(http.StatusServiceUnavailable, false))
	require.True(t, tkAnthropicStubHealthFuseEligible(http.StatusGatewayTimeout, false))
	require.True(t, tkAnthropicStubHealthFuseEligible(529, false))
	require.True(t, tkAnthropicStubHealthFuseEligible(http.StatusTooManyRequests, true))
	require.False(t, tkAnthropicStubHealthFuseEligible(http.StatusTooManyRequests, false))
	require.False(t, tkAnthropicStubHealthFuseEligible(http.StatusBadRequest, false))
	require.False(t, tkAnthropicStubHealthFuseEligible(http.StatusForbidden, false))
	require.False(t, tkAnthropicStubHealthFuseEligible(http.StatusPaymentRequired, false))
}

func TestRateLimitService_HandleUpstreamError_Anthropic400NonClientInduced_DoesNotAdvanceFuse(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}, tierCounts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 2609, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	body := []byte(`{"type":"error","error":{"type":"api_error","message":"internal anomaly"}}`)
	for i := 0; i < 5; i++ {
		require.False(t, service.HandleUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, body),
			"iteration %d: atypical 400 is out of stub-health fuse scope", i)
	}

	require.Equal(t, 0, repo.tempCalls)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs)
}

func TestRateLimitService_HandleUpstreamError_AnthropicExtraUsage429_DoesNotAdvanceFuse(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}, tierCounts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 2610, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	body := []byte(`{"error":{"message":"Third-party apps now draw from your extra usage, not your plan limits."}}`)
	for i := 0; i < 3; i++ {
		require.False(t, service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body))
	}

	require.Equal(t, 0, repo.tempCalls)
	require.Equal(t, 0, repo.setRateLimitedCalls)
	require.Empty(t, counter.incrementIDs)
}

func TestRateLimitService_HandleUpstreamError_Anthropic429WithoutSetRateLimited_DoesNotAdvanceFuse(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 2611, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	body := []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)
	for i := 0; i < 3; i++ {
		service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)
	}
	require.Empty(t, counter.incrementIDs)

	resetAt := time.Now().Add(30 * time.Minute).Unix()
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(resetAt, 10))
	service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, body)
	require.Equal(t, []int64{2611}, counter.incrementIDs, "authoritative 429 must feed the fuse")
}
