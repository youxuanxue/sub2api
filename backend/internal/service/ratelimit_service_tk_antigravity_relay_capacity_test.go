//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

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
				"claude-sonnet-4-5": "claude-sonnet-4-5",
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
	require.True(t, err.ShouldRetryNextAccount())
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

func TestGeminiErrorPolicyInLoop_PostDispatchModelUsesAccountMapping(t *testing.T) {
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

	matched, _ := svc.checkErrorPolicyInLoop(
		context.Background(), antigravityEdgeRelayStub(85), resp, "gemini-3-flash")

	require.True(t, matched)
	require.Equal(t, []string{"gemini-3-flash-tiered"}, counter.modelKeys)
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

func TestAntigravityRelayCapacity_UnresolvedFinalModelDoesNotGuessCooldownScope(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &fakeAntigravitySaturationCounter{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAntigravitySaturationCounter(counter)

	require.True(t, svc.handleAntigravityRelayCapacity(
		context.Background(),
		antigravityEdgeRelayStub(85),
		http.StatusServiceUnavailable,
		antigravityRelayEmptyPoolBody(),
		"unmapped-model",
	))

	require.Empty(t, counter.modelKeys)
	require.Empty(t, repo.modelRateLimitCalls)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setErrorCalls)
}

func TestAntigravityRelayCapacity_UsesPostDispatchAndThinkingFinalModel(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &fakeAntigravitySaturationCounter{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAntigravitySaturationCounter(counter)
	account := antigravityEdgeRelayStub(85)
	ctx := WithThinkingEnabled(context.Background(), true, false)

	require.True(t, svc.handleAntigravityRelayCapacity(
		ctx, account, http.StatusServiceUnavailable, antigravityRelayEmptyPoolBody(), "claude-sonnet-4-5"))

	require.Equal(t, []string{"claude-sonnet-4-5-thinking"}, counter.modelKeys)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestAntigravityRelayCapacity_SustainedEmptyPool_NoModelCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &fakeAntigravitySaturationCounter{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAntigravitySaturationCounter(counter)
	account := antigravityEdgeRelayStub(85)
	body := antigravityRelayEmptyPoolBody()

	for i := 0; i < 4; i++ {
		require.True(t, svc.handleAntigravityRelayCapacity(
			context.Background(), account, http.StatusServiceUnavailable, body, "gemini-3-flash"))
	}
	require.Equal(t, int64(4), counter.counts["gemini-3-flash-tiered"])
	require.Empty(t, repo.modelRateLimitCalls, "relay stub downstream-empty must not write model_rate_limits")
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setErrorCalls)
}
