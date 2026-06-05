//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// G1: tkIsAnthropicClientInducedBadRequest now also recognises request_too_large
// (the 413 error type) so the rarer request_too_large-as-400 shape is exempted via
// the case 400 catch-all, alongside the dedicated case 413 in HandleUpstreamError.
func TestTkIsAnthropicClientInducedBadRequest_RequestTooLarge(t *testing.T) {
	require.True(t, tkIsAnthropicClientInducedBadRequest([]byte(`{"type":"error","error":{"type":"request_too_large","message":"request body too large"}}`)))
	require.True(t, tkIsAnthropicClientInducedBadRequest([]byte(`{"error":{"type":"REQUEST_TOO_LARGE"}}`)))
	// Unchanged behaviour: invalid_request_error still matches; account/server
	// error types still do not.
	require.True(t, tkIsAnthropicClientInducedBadRequest([]byte(`{"error":{"type":"invalid_request_error"}}`)))
	require.False(t, tkIsAnthropicClientInducedBadRequest([]byte(`{"error":{"type":"rate_limit_error"}}`)))
	require.False(t, tkIsAnthropicClientInducedBadRequest([]byte(`{}`)))
}

// G1: an upstream 413 request_too_large is purely caller-fault — the request body
// exceeded Anthropic's own cap after already clearing TokenKey's local body-limit
// middleware. Repeated 413s must NOT advance the per-account cooldown counter or
// pause the account, otherwise a client looping oversized (300KB+) Claude Code
// sessions could cool a shared OAuth account by tripping the 3/3 ladder.
func TestRateLimitService_HandleUpstreamError_Anthropic413_DoesNotPenalizeAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 4131, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	body := []byte(`{"type":"error","error":{"type":"request_too_large","message":"request exceeds the maximum allowed size"}}`)
	for i := 0; i < 5; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusRequestEntityTooLarge, http.Header{}, body)
		require.False(t, shouldDisable, "iteration %d: 413 request_too_large must fail back to the client, not disable the account", i)
	}

	require.Equal(t, 0, repo.tempCalls, "G1: 413 must never write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "G1: 413 must NOT advance the cooldown counter")
}
