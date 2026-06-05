//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTkIsDownstreamAllAccountsExhausted(t *testing.T) {
	// Downstream gateway failover-exhausted envelope (server_error, HTTP 502).
	require.True(t, tkIsDownstreamAllAccountsExhausted("", []byte(`{"error":{"type":"server_error","message":"All available accounts exhausted"}}`)))
	// Already-parsed message, case-insensitive.
	require.True(t, tkIsDownstreamAllAccountsExhausted("ALL AVAILABLE ACCOUNTS EXHAUSTED", nil))
	// Raw infra 5xx and genuine provider errors must NOT match (route-away preserved).
	require.False(t, tkIsDownstreamAllAccountsExhausted("", []byte(`<html><body>502 Bad Gateway</body></html>`)))
	require.False(t, tkIsDownstreamAllAccountsExhausted("", []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)))
	require.False(t, tkIsDownstreamAllAccountsExhausted("", []byte(`{}`)))
}

func TestTkSkipDownstreamFailoverExhaustedPenalty(t *testing.T) {
	body := []byte(`{"error":{"type":"server_error","message":"All available accounts exhausted"}}`)
	// The envelope is written as 502; also accept 429 / other 5xx defensively.
	require.True(t, tkSkipDownstreamFailoverExhaustedPenalty(http.StatusBadGateway, "", body))
	require.True(t, tkSkipDownstreamFailoverExhaustedPenalty(http.StatusTooManyRequests, "", body))
	require.True(t, tkSkipDownstreamFailoverExhaustedPenalty(http.StatusGatewayTimeout, "", body))
	// Non-server status → not in scope.
	require.False(t, tkSkipDownstreamFailoverExhaustedPenalty(http.StatusBadRequest, "", body))
	// Raw infra 5xx with no capacity phrase → NOT skipped (keeps route-away count).
	require.False(t, tkSkipDownstreamFailoverExhaustedPenalty(http.StatusBadGateway, "", []byte(`<html>502 Bad Gateway</html>`)))
	// Genuine provider error → NOT skipped.
	require.False(t, tkSkipDownstreamFailoverExhaustedPenalty(http.StatusTooManyRequests, "", []byte(`{"error":{"type":"rate_limit_error"}}`)))
}

// G2 (narrow): a forwarded "all available accounts exhausted" 502 reaching an
// Anthropic mirror stub is a downstream-pool capacity blip, not stub health.
// Repeated hits must fail over without advancing the 3/3 ladder.
func TestRateLimitService_HandleUpstreamError_DownstreamFailoverExhausted_DoesNotPenalizeStub(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	stub := &Account{ID: 5252, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"pool_mode": true}}

	body := []byte(`{"error":{"type":"server_error","message":"All available accounts exhausted"}}`)
	for i := 0; i < 5; i++ {
		require.True(t, service.HandleUpstreamError(context.Background(), stub, http.StatusBadGateway, http.Header{}, body),
			"iteration %d: downstream failover-exhausted must fail over to the next stub", i)
	}
	require.Equal(t, 0, repo.tempCalls, "G2: downstream failover-exhausted must never write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "G2: downstream failover-exhausted must NOT advance the cooldown counter")
}

// G2 scope guard: a raw edge-infra 502 (Caddy HTML, no TokenKey capacity phrase)
// is NOT a capacity signal — it still counts through the tiered ladder so the
// stub cools briefly and future requests route away from the broken edge
// (PR #333). G2 must not weaken this.
func TestRateLimitService_HandleUpstreamError_RawInfra502_StillCountsAndRoutesAway(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}, tierCounts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	stub := &Account{ID: 5253, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"pool_mode": true}}

	body := []byte(`<html><head><title>502 Bad Gateway</title></head><body>caddy</body></html>`)
	for i := 0; i < 2; i++ {
		require.False(t, service.HandleUpstreamError(context.Background(), stub, http.StatusBadGateway, http.Header{}, body))
	}
	require.True(t, service.HandleUpstreamError(context.Background(), stub, http.StatusBadGateway, http.Header{}, body),
		"3rd raw infra 502 must trip the threshold so the stub cools and routes away")
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, []int64{5253, 5253, 5253}, counter.incrementIDs)
}
