//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func openAIEdgeStub(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api-us6.tokenkey.dev",
		},
	}
}

func grokEdgeStub(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformGrok,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":   "edge-grok-key",
			"base_url":  "https://api-us6.tokenkey.dev",
			"pool_mode": true,
		},
	}
}

func TestTkIsOpenAIEdgeMirrorStub(t *testing.T) {
	require.True(t, tkIsOpenAIEdgeMirrorStub(openAIEdgeStub(63)))
	require.False(t, tkIsOpenAIEdgeMirrorStub(&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
	require.False(t, tkIsOpenAIEdgeMirrorStub(&Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api-us6.tokenkey.dev"}}))
}

func TestTkIsOpenAICompatEdgeMirrorStub_IncludesGrokRelayStub(t *testing.T) {
	require.True(t, tkIsOpenAICompatEdgeMirrorStub(openAIEdgeStub(63)))
	require.True(t, tkIsOpenAICompatEdgeMirrorStub(grokEdgeStub(64)))
	require.False(t, tkIsOpenAICompatEdgeMirrorStub(&Account{ID: 65, Platform: PlatformGrok, Type: AccountTypeOAuth}))
	require.False(t, tkIsOpenAICompatEdgeMirrorStub(&Account{ID: 66, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.x.ai/v1"}}))
}

func TestTkSkipOpenAIDownstreamCapacityPenalty(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	require.True(t, tkSkipOpenAIDownstreamCapacityPenalty(openAIEdgeStub(63), http.StatusTooManyRequests, "", body))
	require.True(t, tkSkipOpenAIDownstreamCapacityPenalty(grokEdgeStub(64), http.StatusTooManyRequests, "", body))
	require.False(t, tkSkipOpenAIDownstreamCapacityPenalty(openAIEdgeStub(63), http.StatusTooManyRequests, "", []byte(`{"error":{"type":"rate_limit_error"}}`)))
	require.False(t, tkSkipOpenAIDownstreamCapacityPenalty(&Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, http.StatusTooManyRequests, "", body))
}

func TestTkSkipOpenAIDownstreamCapacityPenalty_GrokRateLimitEnvelope(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Upstream rate limit exceeded, please retry later"}}`)
	require.True(t, tkSkipOpenAIDownstreamCapacityPenalty(grokEdgeStub(64), http.StatusTooManyRequests, "", body))
	require.Equal(t, "upstream_rate_limit_exhausted", tkOpenAICompatDownstreamCapacityReason(http.StatusTooManyRequests, "", body))
	require.False(t, tkOpenAICompatRetryableOnSameAccount(grokEdgeStub(64), http.StatusTooManyRequests, "", body, false))

	providerBody := []byte(`{"error":{"message":"You have exceeded your current request limit."}}`)
	require.False(t, tkSkipOpenAIDownstreamCapacityPenalty(grokEdgeStub(64), http.StatusTooManyRequests, "", providerBody))
	require.True(t, tkOpenAICompatRetryableOnSameAccount(grokEdgeStub(64), http.StatusTooManyRequests, "", providerBody, false))
}

type fakeOpenAISaturationCounterRL struct {
	incrementIDs []int64
	count        int64
}

func (f *fakeOpenAISaturationCounterRL) IncrementSaturation(_ context.Context, accountID int64, _ int) (int64, error) {
	f.incrementIDs = append(f.incrementIDs, accountID)
	f.count++
	return f.count, nil
}

func (f *fakeOpenAISaturationCounterRL) GetSaturationBatch(_ context.Context, _ []int64) (map[int64]int64, error) {
	return nil, nil
}

func TestRateLimitService_HandleUpstreamError_OpenAIDownstreamNoAvailable_SkipsAndIncrements(t *testing.T) {
	sat := &fakeOpenAISaturationCounterRL{}
	svc := &RateLimitService{}
	svc.SetOpenAISaturationCounter(sat)
	account := openAIEdgeStub(63)
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)
	require.True(t, shouldDisable)
	require.Equal(t, []int64{63}, sat.incrementIDs)
}
