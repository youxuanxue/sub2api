//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type fakeAntigravitySaturationCounter struct {
	ids       []int64
	modelKeys []string
	counts    map[string]int64
}

func (f *fakeAntigravitySaturationCounter) IncrementSaturation(
	_ context.Context,
	accountID int64,
	modelKey string,
	_ int,
) (int64, error) {
	f.ids = append(f.ids, accountID)
	f.modelKeys = append(f.modelKeys, modelKey)
	if f.counts == nil {
		f.counts = make(map[string]int64)
	}
	f.counts[modelKey]++
	return f.counts[modelKey], nil
}

func antigravityEdgeRelayStub(id int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformAntigravity,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"base_url":  "https://api-us6.tokenkey.dev",
			"pool_mode": true,
			"model_mapping": map[string]any{
				"gemini-3-flash":    "gemini-3-flash-tiered",
				"claude-sonnet-4-6": "claude-sonnet-4-6",
			},
		},
	}
}

func antigravityRelayEmptyPoolBody() []byte {
	return []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
}

func TestAntigravityRelayCapacityClassifier_IsNarrow(t *testing.T) {
	body := antigravityRelayEmptyPoolBody()
	relay := antigravityEdgeRelayStub(85)

	require.True(t, tkIsAntigravityRelayCapacityResponse(relay, http.StatusServiceUnavailable, body))
	require.True(t, tkIsAntigravityRelayCapacityResponse(relay, http.StatusTooManyRequests, body))
	require.False(t, tkIsAntigravityRelayCapacityResponse(relay, http.StatusServiceUnavailable,
		[]byte(`{"error":{"message":"provider unavailable"}}`)))
	require.False(t, tkIsAntigravityRelayCapacityResponse(&Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://generativelanguage.googleapis.com",
		},
	}, http.StatusServiceUnavailable, body))
	require.False(t, tkIsAntigravityRelayCapacityResponse(&Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url": "https://api-us6.tokenkey.dev",
		},
	}, http.StatusServiceUnavailable, body))
}

func TestAntigravityRelayCapacityFailoverError_MapsClient429(t *testing.T) {
	err := newUpstreamFailoverErrorWithTKCapacity(
		antigravityEdgeRelayStub(85),
		http.StatusServiceUnavailable,
		http.Header{},
		antigravityRelayEmptyPoolBody(),
	)

	require.Equal(t, http.StatusServiceUnavailable, err.StatusCode)
	require.Equal(t, http.StatusTooManyRequests, err.ClientStatusCode)
	require.Equal(t, AntigravityRelayCapacityClientMessage, err.ClientMessage)
	require.Equal(t, NoAvailableAccountsRetryAfterSeconds, err.ResponseHeaders.Get("Retry-After"))
	require.Equal(t, GatewayFailureScopeAccount, err.Scope)
	require.Equal(t, NextAccountRetry, err.NextAccountAction)
}

func TestGeminiErrorPolicyInLoop_AntigravityRelayCapacityStopsSameAccountRetry(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &fakeAntigravitySaturationCounter{}
	rateLimitSvc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimitSvc.SetAntigravitySaturationCounter(counter)
	svc := &GeminiMessagesCompatService{rateLimitService: rateLimitSvc}
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(antigravityRelayEmptyPoolBody())),
	}

	matched, rebuilt := svc.checkErrorPolicyInLoop(
		context.Background(), antigravityEdgeRelayStub(85), resp, "gemini-3-flash")

	require.True(t, matched)
	require.Equal(t, int64(1), counter.counts["gemini-3-flash-tiered"])
	require.Equal(t, []string{"gemini-3-flash-tiered"}, counter.modelKeys)
	require.Equal(t, http.StatusServiceUnavailable, rebuilt.StatusCode)
	body, err := io.ReadAll(rebuilt.Body)
	require.NoError(t, err)
	require.Equal(t, antigravityRelayEmptyPoolBody(), body)
}

func TestGeminiErrorPolicyInLoop_ArbitraryProvider503DoesNotCountAsCapacity(t *testing.T) {
	counter := &fakeAntigravitySaturationCounter{}
	rateLimitSvc := NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
	rateLimitSvc.SetAntigravitySaturationCounter(counter)
	svc := &GeminiMessagesCompatService{rateLimitService: rateLimitSvc}
	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"provider unavailable"}}`)),
	}

	matched, _ := svc.checkErrorPolicyInLoop(
		context.Background(), antigravityEdgeRelayStub(85), resp, "gemini-3-flash")

	// pool_mode still owns arbitrary provider errors; the capacity hook must not
	// increment its counter or attach capacity metadata.
	require.True(t, matched)
	require.Empty(t, counter.counts)
}

func TestAntigravityRelayCapacity_SaturationThresholdIsPerExactModel(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &fakeAntigravitySaturationCounter{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAntigravitySaturationCounter(counter)
	account := antigravityEdgeRelayStub(85)
	body := antigravityRelayEmptyPoolBody()

	for _, model := range []string{"gemini-3-flash", "gemini-3-flash", "claude-sonnet-4-6"} {
		require.True(t, svc.handleAntigravityRelayCapacity(
			context.Background(), account, http.StatusServiceUnavailable, body, model))
	}

	require.Empty(t, repo.modelRateLimitCalls)
	require.Equal(t, int64(2), counter.counts["gemini-3-flash-tiered"])
	require.Equal(t, int64(1), counter.counts["claude-sonnet-4-6"])
}

func TestAntigravityRelayCapacity_ThresholdWritesExactModelOnly(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &fakeAntigravitySaturationCounter{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAntigravitySaturationCounter(counter)
	account := antigravityEdgeRelayStub(85)
	body := antigravityRelayEmptyPoolBody()

	for i := 0; i < 2; i++ {
		require.True(t, svc.handleAntigravityRelayCapacity(
			context.Background(), account, http.StatusServiceUnavailable, body, "gemini-3-flash"))
	}
	require.Empty(t, repo.modelRateLimitCalls)

	require.True(t, svc.handleAntigravityRelayCapacity(
		context.Background(), account, http.StatusServiceUnavailable, body, "gemini-3-flash"))
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, int64(85), call.accountID)
	require.Equal(t, "gemini-3-flash-tiered", call.scope)
	require.Equal(t, tkAntigravityRelayDownstreamEmptyReason, call.reason)
	require.WithinDuration(t, time.Now().Add(90*time.Second), call.resetAt, 3*time.Second)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setErrorCalls)

	cooled := antigravityEdgeRelayStub(85)
	cooled.Extra = map[string]any{
		modelRateLimitsKey: map[string]any{
			call.scope: map[string]any{
				"rate_limit_reset_at": call.resetAt.Format(time.RFC3339),
			},
		},
	}
	require.False(t, cooled.IsSchedulableForModelWithContext(context.Background(), "gemini-3-flash"))
	require.True(t, cooled.IsSchedulableForModelWithContext(context.Background(), "claude-sonnet-4-6"))
}
