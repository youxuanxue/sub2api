//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Official OpenAI help-page 401 on a foreign (non-official) credential must
// short-circuit without disabling — same contract as the pre-companion inline
// chain in HandleUpstreamError.
func TestTkTryHandleUpstreamErrorPrelude_ForeignCredentialSkipsDisable(t *testing.T) {
	svc := NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       91,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1",
		},
	}
	body := []byte(`Incorrect API key provided: sk-****. You can find your API key at https://platform.openai.com/account/api-keys.`)

	handled, shouldDisable := svc.tkTryHandleUpstreamErrorPrelude(
		context.Background(), account, http.StatusUnauthorized, nil, body, "")
	require.True(t, handled)
	require.False(t, shouldDisable)
}

// Mirror-stub "no available accounts" capacity must be handled by the prelude
// (and still return shouldDisable=true for request failover) without writing
// account error / temp-unschedulable state.
func TestTkTryHandleUpstreamErrorPrelude_OpenAIDownstreamCapacity(t *testing.T) {
	sat := &fakeOpenAISaturationCounterRL{}
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetOpenAISaturationCounter(sat)
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)

	handled, shouldDisable := svc.tkTryHandleUpstreamErrorPrelude(
		context.Background(), openAIEdgeStub(63), http.StatusTooManyRequests, nil, body, "")
	require.True(t, handled)
	require.True(t, shouldDisable)
	require.Equal(t, []int64{63}, sat.incrementIDs)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.tempCalls)
}

func TestTkTryHandleUpstreamErrorPrelude_UnhandledContinues(t *testing.T) {
	svc := NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	handled, shouldDisable := svc.tkTryHandleUpstreamErrorPrelude(
		context.Background(), account, http.StatusBadGateway, nil, []byte(`upstream boom`), "")
	require.False(t, handled)
	require.False(t, shouldDisable)
}

func TestTkTryHandleUpstreamErrorPrelude_NilGuards(t *testing.T) {
	var svc *RateLimitService
	handled, shouldDisable := svc.tkTryHandleUpstreamErrorPrelude(
		context.Background(), &Account{ID: 1}, http.StatusInternalServerError, nil, nil, "")
	require.True(t, handled)
	require.False(t, shouldDisable)

	svc = NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
	handled, shouldDisable = svc.tkTryHandleUpstreamErrorPrelude(
		context.Background(), nil, http.StatusInternalServerError, nil, nil, "")
	require.True(t, handled)
	require.False(t, shouldDisable)
}
