//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Sep 25 Codex outage shape: masked sk-svcacct echo with invariant fvMA suffix.
const tkOpenAICodexBackendSvcacct401IncidentBody = `{"error":{"message":"Incorrect API key provided: sk-svcac***********************************************************************************************************************************************************fvMA. You can find your API key at https://platform.openai.com/account/api-keys.","type":"invalid_request_error","param":null,"code":"invalid_api_key"}}`

func TestTkIsOpenAICodexBackendSvcacct401(t *testing.T) {
	t.Parallel()

	require.True(t, tkIsOpenAICodexBackendSvcacct401(401, []byte(tkOpenAICodexBackendSvcacct401IncidentBody)))
	require.True(t, tkIsOpenAICodexBackendSvcacct401(401, nil,
		"Incorrect API key provided: sk-svcacct-abc***fvMA. You can find your API key at https://platform.openai.com/account/api-keys."))
	require.True(t, tkIsOpenAICodexBackendSvcacct401(401,
		[]byte(`Incorrect API key provided: sk-svcacct-test. You can find your API key at https://platform.openai.com/account/api-keys.`)))

	// Genuine per-account / other shapes must NOT match.
	require.False(t, tkIsOpenAICodexBackendSvcacct401(401,
		[]byte(`{"error":{"message":"Incorrect API key provided: sk-proj-abc. You can find your API key at https://platform.openai.com/account/api-keys.","code":"invalid_api_key"}}`)))
	require.False(t, tkIsOpenAICodexBackendSvcacct401(401,
		[]byte(`{"error":{"code":"token_revoked","message":"Encountered invalidated oauth token for user, failing request"}}`)))
	require.False(t, tkIsOpenAICodexBackendSvcacct401(401, []byte(`{"detail":"Unauthorized"}`)))
	require.False(t, tkIsOpenAICodexBackendSvcacct401(401, []byte(tkCapabilityScope401IncidentBody)))
	require.False(t, tkIsOpenAICodexBackendSvcacct401(403, []byte(tkOpenAICodexBackendSvcacct401IncidentBody)))
	require.False(t, tkIsOpenAICodexBackendSvcacct401(401, nil))
}

func TestRateLimitService_HandleUpstreamError_OpenAICodexBackendSvcacct401_DoesNotDisable(t *testing.T) {
	t.Parallel()

	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       911,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		401,
		http.Header{},
		[]byte(tkOpenAICodexBackendSvcacct401IncidentBody),
	)

	require.False(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
}

func TestRateLimitService_OAuth401_OpenAIValidTokenStillDisablesOnGenericUnauthorized(t *testing.T) {
	t.Parallel()

	// Regression: generic still-valid-token 401 must keep first-strike SetError.
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       912,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
}

func TestTkIsRecoverableOpenAI401_CodexBackendSvcacctIsNotRecoverable(t *testing.T) {
	t.Parallel()

	require.False(t, tkIsRecoverableOpenAI401(http.StatusUnauthorized, []byte(tkOpenAICodexBackendSvcacct401IncidentBody)))
	// Ordinary invalid_api_key without sk-svcac prefix remains refresh-eligible.
	require.True(t, tkIsRecoverableOpenAI401(http.StatusUnauthorized, []byte(`{"error":{"message":"invalid_api_key"}}`)))
}

func TestOpenAIGatewayService_ShouldFailover_CodexBackendSvcacct401IsSharedFault(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{}
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(
		&Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		401,
		"Incorrect API key provided: sk-svcacct…fvMA",
		[]byte(tkOpenAICodexBackendSvcacct401IncidentBody),
	))
}
