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

func TestTkIsOpenAIEdgeMirrorStub(t *testing.T) {
	require.True(t, tkIsOpenAIEdgeMirrorStub(openAIEdgeStub(63)))
	require.False(t, tkIsOpenAIEdgeMirrorStub(&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}))
	require.False(t, tkIsOpenAIEdgeMirrorStub(&Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api-us6.tokenkey.dev"}}))
}

func TestTkSkipOpenAIDownstreamCapacityPenalty(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	require.True(t, tkSkipOpenAIDownstreamCapacityPenalty(openAIEdgeStub(63), http.StatusTooManyRequests, "", body))
	require.False(t, tkSkipOpenAIDownstreamCapacityPenalty(openAIEdgeStub(63), http.StatusTooManyRequests, "", []byte(`{"error":{"type":"rate_limit_error"}}`)))
	require.False(t, tkSkipOpenAIDownstreamCapacityPenalty(&Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth}, http.StatusTooManyRequests, "", body))
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
