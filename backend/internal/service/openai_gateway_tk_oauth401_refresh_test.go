//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

func newOpenAI401OAuthAccount(id int64, accessToken string) *Account {
	return &Account{
		ID:          id,
		Name:        "openai-oauth-401-ssot",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  accessToken,
			"refresh_token": "rt-ssot",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
}

func TestOpenAITokenProvider_ForceRefresh_LockHeldAdoptsWinnerToken(t *testing.T) {
	stale := newOpenAI401OAuthAccount(5610, "stale-token")
	winner := newOpenAI401OAuthAccount(5610, "winner-token")
	repo := &refreshAPIAccountRepo{account: winner}
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false
	cache.tokens[OpenAITokenCacheKey(stale)] = "stale-token"
	executor := &refreshAPIExecutorStub{needsRefresh: false}

	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)

	refreshed, err := provider.ForceRefresh(context.Background(), stale)
	require.NoError(t, err)
	require.Equal(t, "winner-token", refreshed.GetOpenAIAccessToken())
	require.Equal(t, 0, executor.refreshCalls, "输家不得自己再刷一次")
}

func TestOpenAITokenProvider_ForceRefresh_LockHeldStaleTokenIsFailure(t *testing.T) {
	account := newOpenAI401OAuthAccount(5611, "stale-token")
	repo := &refreshAPIAccountRepo{account: account}
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false
	cache.tokens[OpenAITokenCacheKey(account)] = "stale-token"
	executor := &refreshAPIExecutorStub{needsRefresh: false}

	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)

	_, err := provider.ForceRefresh(context.Background(), account)
	require.Error(t, err)
	require.Contains(t, err.Error(), "lock held")
	require.Equal(t, 0, executor.refreshCalls)
}

func TestOpenAIGatewayService_Forward_OAuth401LockHeldUsesWinnerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	stale := newOpenAI401OAuthAccount(5612, "stale-token")
	winner := newOpenAI401OAuthAccount(5612, "winner-token")
	repo := &refreshAPIAccountRepo{account: winner}
	cache := newOpenAITokenCacheStub()
	cache.lockAcquired = false
	executor := &refreshAPIExecutorStub{needsRefresh: false}
	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)

	rateRepo := &rateLimitAccountRepoStub{}
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
					`{"id":"resp_oauth_401_lock","usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`,
				)),
			},
		},
	}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := &OpenAIGatewayService{
		cfg:                 cfg,
		httpUpstream:        upstream,
		openAITokenProvider: provider,
		rateLimitService:    &RateLimitService{accountRepo: rateRepo},
	}

	result, err := svc.Forward(context.Background(), c, stale, []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"hello"}]}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.callCount)
	require.Equal(t, "Bearer stale-token", upstream.reqs[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer winner-token", upstream.reqs[1].Header.Get("Authorization"))
	require.Zero(t, rateRepo.setErrorCalls)
	require.Equal(t, 0, executor.refreshCalls)
}

func TestOpenAIGatewayService_recoverOpenAIWS401AccessToken(t *testing.T) {
	account := newOpenAI401OAuthAccount(5613, "stale-token")
	repo := &refreshAPIAccountRepo{account: account}
	cache := newOpenAITokenCacheStub()
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "fresh-token",
			"refresh_token": "rt-ssot",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)
	svc := &OpenAIGatewayService{openAITokenProvider: provider}

	headers := http.Header{"Authorization": []string{"Bearer stale-token"}}
	refreshed, token, ok := svc.recoverOpenAIWS401AccessToken(
		context.Background(),
		account,
		http.StatusUnauthorized,
		[]byte(`{"error":{"message":"invalid_api_key"}}`),
		headers,
	)
	require.True(t, ok)
	require.Equal(t, "fresh-token", token)
	require.Equal(t, "Bearer fresh-token", refreshed.Get("Authorization"))
	require.Equal(t, 1, executor.refreshCalls)

	_, _, ok = svc.recoverOpenAIWS401AccessToken(
		context.Background(),
		account,
		http.StatusUnauthorized,
		[]byte(`{"error":{"code":"token_revoked","message":"revoked"}}`),
		headers,
	)
	require.False(t, ok)
	require.Equal(t, 1, executor.refreshCalls, "永久 401 不得再走 RefreshNow")
}

type openAIWS401ThenOKDialer struct {
	mu        sync.Mutex
	conn      openAIWSClientConn
	auths     []string
	dialCount int
}

func (d *openAIWS401ThenOKDialer) Dial(
	_ context.Context,
	_ string,
	headers http.Header,
	_ string,
) (openAIWSClientConn, int, http.Header, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialCount++
	d.auths = append(d.auths, headers.Get("Authorization"))
	if d.dialCount == 1 {
		return nil, http.StatusUnauthorized, nil, &openAIWSHandshakeError{
			Body: []byte(`{"error":{"message":"invalid_api_key"}}`),
			Err:  errors.New("unauthorized"),
		}
	}
	return d.conn, http.StatusSwitchingProtocols, http.Header{}, nil
}

func TestOpenAIGatewayService_Forward_WSv2OAuth401RefreshesOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")

	account := newOpenAI401OAuthAccount(5614, "stale-token")
	account.Extra = map[string]any{"openai_oauth_responses_websockets_v2_enabled": true}
	repo := &refreshAPIAccountRepo{account: account}
	cache := newOpenAITokenCacheStub()
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "fresh-token",
			"refresh_token": "rt-ssot",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ws_401","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWS401ThenOKDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	rateRepo := &rateLimitAccountRepoStub{}
	svc := &OpenAIGatewayService{
		cfg:                 cfg,
		httpUpstream:        &httpUpstreamRecorder{},
		cache:               &stubGatewayCache{},
		openaiWSResolver:    NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:       NewCodexToolCorrector(),
		openaiWSPool:        pool,
		openAITokenProvider: provider,
		rateLimitService:    &RateLimitService{accountRepo: rateRepo},
	}

	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"input_text","text":"hello"}]}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "resp_ws_401", result.RequestID)
	require.Equal(t, 2, dialer.dialCount)
	require.Equal(t, []string{"Bearer stale-token", "Bearer fresh-token"}, dialer.auths)
	require.Zero(t, rateRepo.setErrorCalls)
	require.Equal(t, 1, executor.refreshCalls)
}

func TestOpenAIGatewayService_PassthroughWS_OAuth401RefreshesOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newOpenAI401OAuthAccount(5615, "stale-token")
	account.Extra = map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough}
	repo := &refreshAPIAccountRepo{account: account}
	cache := newOpenAITokenCacheStub()
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "fresh-token",
			"refresh_token": "rt-ssot",
			"expires_at":    time.Now().Add(2 * time.Hour).Format(time.RFC3339),
		},
	}
	provider := NewOpenAITokenProvider(repo, cache, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, cache), executor)

	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough

	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_ws_pt_401","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	dialer := &openAIWS401ThenOKDialer{conn: upstream}
	rateRepo := &rateLimitAccountRepoStub{}
	svc := &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: dialer,
		openAITokenProvider:       provider,
		rateLimitService:          &RateLimitService{accountRepo: rateRepo},
	}

	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	server, serverErr := startOpenAIWS401PassthroughServer(t, controlCtx, svc, account, "stale-token")
	defer server.Close()

	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	event, err := readPassthroughLifecycleFrame(t, clientConn, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(event, "type").String())
	_ = clientConn.CloseNow()
	_ = upstream.Close()
	require.Equal(t, 2, dialer.dialCount)
	require.Equal(t, []string{"Bearer stale-token", "Bearer fresh-token"}, dialer.auths)
	require.Zero(t, rateRepo.setErrorCalls)
	select {
	case err := <-serverErr:
		if err != nil {
			require.ErrorIs(t, err, errOpenAIWSConnClosed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("passthrough WS server did not stop after both connections closed")
	}
}

func TestOpenAIGatewayService_PassthroughWS_OAuth401RevokedDoesNotRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := newOpenAI401OAuthAccount(5616, "revoked-token")
	account.Extra = map[string]any{"openai_oauth_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough}
	repo := &refreshAPIAccountRepo{account: account}
	executor := &refreshAPIExecutorStub{needsRefresh: false, credentials: map[string]any{"access_token": "should-not-use"}}
	provider := NewOpenAITokenProvider(repo, newOpenAITokenCacheStub(), nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), executor)

	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModePassthrough

	dialer := &openAIWSAlways401Dialer{body: []byte(`{"error":{"code":"token_revoked","message":"revoked"}}`)}
	svc := &OpenAIGatewayService{
		cfg:                       cfg,
		httpUpstream:              &httpUpstreamRecorder{},
		cache:                     &stubGatewayCache{},
		openaiWSResolver:          NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:             NewCodexToolCorrector(),
		openaiWSPassthroughDialer: dialer,
		openAITokenProvider:       provider,
		rateLimitService:          &RateLimitService{accountRepo: &rateLimitAccountRepoStub{}},
	}

	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	defer cancelControl(context.Canceled)
	server, serverErr := startOpenAIWS401PassthroughServer(t, controlCtx, svc, account, "revoked-token")
	defer server.Close()

	clientConn := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = clientConn.CloseNow() }()

	select {
	case err := <-serverErr:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("revoked WS 401 应立即失败")
	}
	require.Equal(t, 1, dialer.dialCount)
	require.Equal(t, 0, executor.refreshCalls)
}

func startOpenAIWS401PassthroughServer(
	t *testing.T,
	controlCtx context.Context,
	svc *OpenAIGatewayService,
	account *Account,
	token string,
) (*httptest.Server, <-chan error) {
	t.Helper()
	serverErr := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()

		msgType, firstMessage, err := ReadOpenAIWSClientMessage(
			controlCtx,
			conn,
			3*time.Second,
			coderws.StatusPolicyViolation,
			"missing first response.create message",
		)
		if err != nil {
			serverErr <- err
			return
		}
		if msgType != coderws.MessageText {
			serverErr <- errors.New("first message was not text")
			return
		}

		recorder := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(recorder)
		req := r.Clone(controlCtx)
		req.Header = req.Header.Clone()
		ginCtx.Request = req
		serverErr <- svc.ProxyResponsesWebSocketFromClient(controlCtx, ginCtx, conn, account, token, firstMessage, nil)
	}))
	return server, serverErr
}

type openAIWSAlways401Dialer struct {
	mu        sync.Mutex
	body      []byte
	dialCount int
}

func (d *openAIWSAlways401Dialer) Dial(
	_ context.Context,
	_ string,
	_ http.Header,
	_ string,
) (openAIWSClientConn, int, http.Header, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialCount++
	return nil, http.StatusUnauthorized, nil, &openAIWSHandshakeError{
		Body: append([]byte(nil), d.body...),
		Err:  errors.New("unauthorized"),
	}
}
