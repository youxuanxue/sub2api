//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCreateTestPayloadOmitsTemperature(t *testing.T) {
	payload, err := createTestPayload("claude-opus-4-8")
	require.NoError(t, err)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(raw, "temperature").Exists(),
		"OAuth Claude Code probe must omit temperature (#8625)")
	require.Equal(t, "claude-opus-4-8", gjson.GetBytes(raw, "model").String())
	require.True(t, gjson.GetBytes(raw, "stream").Bool())
}

type oauthProbeAccountRepo struct {
	refreshAPIAccountRepo
	setErrorCalls int
	setErrorMsg   string
}

func (r *oauthProbeAccountRepo) SetError(_ context.Context, _ int64, errorMsg string) error {
	r.setErrorCalls++
	r.setErrorMsg = errorMsg
	return nil
}

func TestOpenAIAccountConnection_OAuth401ForceRefreshBeforeSetError(t *testing.T) {
	account := &Account{
		ID:          8855,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "stale-token",
			"refresh_token": "rt-8855",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	repo := &oauthProbeAccountRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: account}}
	cache := newOpenAITokenCacheStub()
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "fresh-token",
			"refresh_token": "rt-8855",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)

	upstream := &queuedHTTPUpstream{responses: []*http.Response{
		newJSONResponse(http.StatusUnauthorized, `{"error":{"message":"invalid_api_key"}}`),
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\"}}\n\n",
			)),
		},
	}}
	gateway := &OpenAIGatewayService{
		openAITokenProvider: provider,
		accountRepo:         repo,
	}
	svc := &AccountTestService{
		accountRepo:          repo,
		httpUpstream:         upstream,
		cfg:                  &config.Config{},
		openAIGatewayService: gateway,
	}

	c, recorder := newTestContext()
	err := svc.testOpenAIAccountConnection(c, account, "gpt-5.4", "", AccountTestModeDefault)
	require.NoError(t, err)
	require.Equal(t, 1, executor.refreshCalls)
	require.Equal(t, 0, repo.setErrorCalls, "recoverable OAuth 401 must refresh instead of SetError (#8855)")
	require.Contains(t, recorder.Body.String(), "test_complete")
	require.Equal(t, "fresh-token", account.GetOpenAIAccessToken())
}

func TestOpenAIAccountConnection_OAuth401PermanentStillSetError(t *testing.T) {
	account := &Account{
		ID:          8856,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "stale-token",
			"refresh_token": "rt-8856",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	repo := &oauthProbeAccountRepo{refreshAPIAccountRepo: refreshAPIAccountRepo{account: account}}
	provider := NewOpenAITokenProvider(repo, newOpenAITokenCacheStub(), nil)
	upstream := &queuedHTTPUpstream{responses: []*http.Response{
		newJSONResponse(http.StatusUnauthorized, `{"error":{"code":"token_revoked","message":"revoked"}}`),
	}}
	gateway := &OpenAIGatewayService{
		openAITokenProvider: provider,
		accountRepo:         repo,
	}
	svc := &AccountTestService{
		accountRepo:          repo,
		httpUpstream:         upstream,
		cfg:                  &config.Config{},
		openAIGatewayService: gateway,
	}

	c, _ := newTestContext()
	err := svc.testOpenAIAccountConnection(c, account, "gpt-5.4", "", AccountTestModeDefault)
	require.Error(t, err)
	require.Equal(t, 1, repo.setErrorCalls, "permanent 401 must still SetError")
	require.Contains(t, repo.setErrorMsg, "Authentication failed (401)")
}
