//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkIsRecoverableOpenAI401(t *testing.T) {
	t.Parallel()

	require.True(t, tkIsRecoverableOpenAI401(http.StatusUnauthorized, []byte(`{"error":{"message":"invalid_api_key"}}`)))
	require.False(t, tkIsRecoverableOpenAI401(http.StatusForbidden, []byte(`{"error":{"message":"invalid_api_key"}}`)))
	require.False(t, tkIsRecoverableOpenAI401(http.StatusUnauthorized, []byte(`{"error":{"code":"token_revoked","message":"revoked"}}`)))
	require.False(t, tkIsRecoverableOpenAI401(http.StatusUnauthorized, []byte(`{"error":{"code":"token_invalidated","message":"invalidated"}}`)))
	require.False(t, tkIsRecoverableOpenAI401(http.StatusUnauthorized, []byte(`{"detail":"Unauthorized"}`)))
	require.False(t, tkIsRecoverableOpenAI401(http.StatusUnauthorized, []byte(tkCapabilityScope401IncidentBody)))
}

func TestOpenAITokenProvider_ForceRefresh_BypassesExpirySkewAndClearsCache(t *testing.T) {
	account := &Account{
		ID:       5542,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token":  "stale-token",
			"refresh_token": "rt-5542",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	repo := &refreshAPIAccountRepo{account: account}
	cache := newOpenAITokenCacheStub()
	cache.tokens[OpenAITokenCacheKey(account)] = "stale-token"
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "fresh-token",
			"refresh_token": "rt-5542",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}

	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)

	refreshed, err := provider.ForceRefresh(context.Background(), account)
	require.NoError(t, err)
	require.NotNil(t, refreshed)
	require.Equal(t, "fresh-token", refreshed.GetOpenAIAccessToken())
	require.Equal(t, 1, executor.refreshCalls)
	_, cached := cache.tokens[OpenAITokenCacheKey(account)]
	require.False(t, cached, "强制刷新后不得继续命中过期 cache")
}

func TestOpenAITokenProvider_ForceRefresh_MissingRefreshToken(t *testing.T) {
	account := &Account{
		ID:       5543,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token": "stale-token",
			"expires_at":   time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	provider := NewOpenAITokenProvider(nil, nil, nil)
	_, err := provider.ForceRefresh(context.Background(), account)
	require.Error(t, err)
}

func TestOpenAIGatewayService_Forward_OAuth401RefreshesOnceAndSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "custom-client/1.0")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	account := &Account{
		ID:          5544,
		Name:        "openai-oauth-401",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "stale-token",
			"refresh_token": "rt-5544",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	repo := &refreshAPIAccountRepo{account: account}
	cache := newOpenAITokenCacheStub()
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "fresh-token",
			"refresh_token": "rt-5544",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)

	upstream := &httpUpstreamSequenceRecorder{
		responses: []*http.Response{
			{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"invalid_api_key"}}`)),
			},
			{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"resp_oauth_401_retry","usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`,
				)),
			},
		},
	}

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	rateRepo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{
		cfg:                 cfg,
		httpUpstream:        upstream,
		openAITokenProvider: provider,
		rateLimitService:    &RateLimitService{accountRepo: rateRepo},
	}

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"hello"}]}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.callCount)
	require.Equal(t, "Bearer stale-token", upstream.reqs[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer fresh-token", upstream.reqs[1].Header.Get("Authorization"))
	require.Zero(t, rateRepo.tempCalls)
}

func TestOpenAIGatewayService_Forward_OAuth401RevokedDoesNotRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	account := &Account{
		ID:          5545,
		Name:        "openai-oauth-revoked",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "revoked-token",
			"refresh_token": "rt-5545",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	repo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials:  map[string]any{"access_token": "should-not-use"},
	}
	provider := NewOpenAITokenProvider(repo, newOpenAITokenCacheStub(), nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), executor)

	upstream := &httpUpstreamSequenceRecorder{
		responses: []*http.Response{{
			StatusCode: http.StatusUnauthorized,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"token_revoked","message":"token revoked"}}`)),
		}},
	}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	svc := &OpenAIGatewayService{
		cfg:                 cfg,
		httpUpstream:        upstream,
		openAITokenProvider: provider,
		rateLimitService:    &RateLimitService{accountRepo: &modelNotFoundAccountRepoStub{}},
	}

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"hello"}]}`))
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, 1, upstream.callCount)
	require.Equal(t, 0, executor.refreshCalls)
}
