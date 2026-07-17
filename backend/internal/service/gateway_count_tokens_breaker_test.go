//go:build unit

package service

// Regression tests for count_tokens 400-exemption from the
// anthropic_upstream_error breaker (see PR #280 / incident 2026-05-18).
//
// Lives in a separate file with `//go:build unit` because it depends on
// rateLimitAccountRepoStub + anthropicUpstreamErrorCounterCacheStub, which
// are themselves declared in unit-only files. The companion
// gateway_anthropic_apikey_passthrough_test.go is intentionally untagged
// so its tests run under both -tags=unit and -tags=integration; moving
// these two regressions out keeps that contract intact.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestGatewayService_ForwardCountTokens_400DoesNotTripUpstreamErrorBreaker
// 回归：count_tokens 端的 400（Anthropic 返回 invalid_request_error，常见客户
// 端 schema bug 如带 `temperature` 字段）必须 *不* 进 RateLimitService 的
// anthropic_upstream_error 计数器，避免一个客户端 bug 把整个账号搞 temp_unschedulable
// 10 分钟（生产事故 2026-05-18）。
func TestGatewayService_ForwardCountTokens_400DoesNotTripUpstreamErrorBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	// 即便客户端送的 body 本身没有 unsupported 字段，中继站/上游也可能因别的
	// 原因返回 400；这种 400 同样不应该熔断 account。
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-7"}

	upstreamRespBody := `{"type":"error","error":{"type":"invalid_request_error","message":"temperature: Extra inputs are not permitted"}}`
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamRespBody)),
		},
	}

	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}}
	repo := &rateLimitAccountRepoStub{}
	rateLimit := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimit.SetAnthropicUpstreamErrorCounterCache(counter)

	svc := &GatewayService{
		cfg:              &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:     upstream,
		rateLimitService: rateLimit,
	}

	account := &Account{
		ID:          501,
		Name:        "ct-400-no-breaker",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "k",
			"base_url": "https://api.anthropic.com",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	err := svc.ForwardCountTokens(context.Background(), c, account, parsed)
	require.Error(t, err)
	require.Contains(t, err.Error(), "upstream error: 400")
	require.Empty(t, counter.incrementIDs, "count_tokens 400 must not increment the anthropic_upstream_error counter")
	require.Equal(t, 0, repo.tempCalls, "count_tokens 400 must not mark account temp_unschedulable")
}

// TestGatewayService_ForwardCountTokens_Wrapped404ReturnsLocalEstimate 验证 #656 主路径短路：
// 中转站不支持 count_tokens 端点、把 Spring NoResourceFoundException 包装进非标准的
// HTTP 400 返回时，ForwardCountTokens 应在熔断/failover **之前**短路，返回本地估算，
// 且既不熔断也不罚下账号。
// 这条覆盖的是 #656 的主路径接线（谓词本身由 TestIsCountTokensUnsupported404 覆盖）。
func TestGatewayService_ForwardCountTokens_Wrapped404ReturnsLocalEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-7"}

	// 中转站把 Spring NoResourceFoundException / 404 NOT_FOUND 包装进 HTTP 400（非标准）。
	upstreamRespBody := `{"error":{"message":"404 NOT_FOUND \"NoResourceFoundException: No static resource v1/messages/count_tokens.\""}}`
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(upstreamRespBody)),
		},
	}

	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}}
	repo := &rateLimitAccountRepoStub{}
	rateLimit := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimit.SetAnthropicUpstreamErrorCounterCache(counter)

	svc := &GatewayService{
		cfg:              &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:     upstream,
		rateLimitService: rateLimit,
	}

	account := &Account{
		ID:          502,
		Name:        "ct-wrapped-404",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "k",
			"base_url": "https://relay.example.com",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	err := svc.ForwardCountTokens(context.Background(), c, account, parsed)
	require.NoError(t, err, "wrapped-404 应短路并返回 nil（已写估算响应），不作为错误冒泡")
	require.Equal(t, http.StatusOK, rec.Code, "应短路返回本地估算")
	require.Greater(t, int(gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Int()), 0)
	require.Empty(t, counter.incrementIDs, "端点不支持不应熔断账号（短路在熔断之前）")
	require.Equal(t, 0, repo.tempCalls, "端点不支持不应 temp_unschedulable 账号")
}

// TestGatewayService_ForwardCountTokens_CapacityErrorsFailoverNoBreaker
// 契约更新（count_tokens failover 修复，见 gateway_handler_tk_count_tokens_failover.go）：
// count_tokens 是请求前预检端点，其 429/529 容量类错误现在
//
//	(a) **不再**计入 anthropic_upstream_error breaker（不熔断主力账号——一次预检的
//	    529 把主力账号 temp_unschedulable/overload 10 分钟会拖垮整组；现场 edge us1
//	    acct1 因单次 count_tokens 529 被罚下、acct4 空闲）；
//	(b) 返回 *UpstreamFailoverError 且**不写客户端响应**，交由 handler 的 failover
//	    loop 换号 / 池内轮换。
//
// 这反转了旧测试 “429StillTripsUpstreamErrorBreaker” 的契约：轮换交给 failover，
// 状态写入交给真正的 /v1/messages 路径。
func TestGatewayService_ForwardCountTokens_CapacityErrorsFailoverNoBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name    string
		status  int
		respMsg string
	}{
		{name: "429 rate limit", status: http.StatusTooManyRequests, respMsg: "rate_limit_error"},
		{name: "529 overloaded", status: 529, respMsg: "overloaded_error"},
		// 503（pool stub prod→edge 透回的 no-available-accounts）与主路径口径对齐：
		// transient 容量错误交 failover 轮换，不熔断主力账号。
		{name: "503 unavailable", status: http.StatusServiceUnavailable, respMsg: "overloaded_error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

			body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
			parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-7"}

			upstreamRespBody := `{"type":"error","error":{"type":"` + tc.respMsg + `","message":"x"}}`
			upstream := &anthropicHTTPUpstreamRecorder{
				resp: &http.Response{
					StatusCode: tc.status,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(upstreamRespBody)),
				},
			}

			counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1}}
			repo := &rateLimitAccountRepoStub{}
			rateLimit := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			rateLimit.SetAnthropicUpstreamErrorCounterCache(counter)

			svc := &GatewayService{
				cfg:              &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
				httpUpstream:     upstream,
				rateLimitService: rateLimit,
			}

			account := &Account{
				ID:          502,
				Name:        "ct-capacity-failover",
				Platform:    PlatformAnthropic,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
				Credentials: map[string]any{
					"api_key":  "k",
					"base_url": "https://api.anthropic.com",
				},
				Status:      StatusActive,
				Schedulable: true,
			}

			err := svc.ForwardCountTokens(context.Background(), c, account, parsed)

			// (b) 返回 *UpstreamFailoverError，状态码透传。
			var fe *UpstreamFailoverError
			require.ErrorAs(t, err, &fe)
			require.Equal(t, tc.status, fe.StatusCode)
			// 非 pool_mode 账号 → 普通换号（不在同账号上重试）。
			require.False(t, fe.RetryableOnSameAccount)
			// 未向客户端写入响应（默认 200，交由 handler 耗尽时回写）。
			require.Equal(t, http.StatusOK, rec.Code, "count_tokens failover must not write a client response at service layer")
			// (a) 不计入 breaker。
			require.Empty(t, counter.incrementIDs, "count_tokens %d must not feed the anthropic_upstream_error breaker", tc.status)
			require.Equal(t, 0, repo.tempCalls, "count_tokens %d must not mark account temp_unschedulable", tc.status)
		})
	}
}

// TestGatewayService_WriteCountTokensFailoverError 锁定 failover 耗尽时回写的
// count_tokens 错误形状（{type:error,error:{type,message}}）与状态码/文案映射。
func TestGatewayService_WriteCountTokensFailoverError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GatewayService{}

	cases := []struct {
		status  int
		wantMsg string
	}{
		{status: http.StatusTooManyRequests, wantMsg: "Rate limit exceeded"},
		{status: 529, wantMsg: "Service overloaded"},
		{status: http.StatusInternalServerError, wantMsg: "Upstream request failed"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		svc.WriteCountTokensFailoverError(c, &UpstreamFailoverError{StatusCode: tc.status})
		require.Equal(t, tc.status, rec.Code)
		require.Equal(t, "error", gjson.GetBytes(rec.Body.Bytes(), "type").String())
		require.Equal(t, "upstream_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
		require.Equal(t, tc.wantMsg, gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	}

	// nil failoverErr 兜底为 502。
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	svc.WriteCountTokensFailoverError(c, nil)
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

// TestGatewayService_ForwardCountTokens_PoolModeRetryableStatus
// pool_mode stub 把 529 显式配进 pool_mode_retry_status_codes 时，count_tokens 的
// 529 失败应标记 RetryableOnSameAccount=true，使 handler 在同一 stub 上重试
// （= 池内轮换到下个上游成员），而不是直接换 prod 账号。
func TestGatewayService_ForwardCountTokens_PoolModeRetryableStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-7"}

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: 529,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"overloaded_error","message":"x"}}`)),
		},
	}

	svc := &GatewayService{
		cfg:              &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:     upstream,
		rateLimitService: &RateLimitService{},
	}

	account := &Account{
		ID:          701,
		Name:        "cc-us1-pool-stub",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":                      "k",
			"base_url":                     "https://api-us1.tokenkey.dev",
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{float64(529), float64(503)},
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	err := svc.ForwardCountTokens(context.Background(), c, account, parsed)
	var fe *UpstreamFailoverError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, 529, fe.StatusCode)
	require.True(t, fe.RetryableOnSameAccount, "pool_mode stub with 529 in retry codes must retry same account (pool rotation)")
}

// TestGatewayService_ForwardCountTokens_OAuthMimicInjectionGetsStripped
// 回归：OAuth 账号走 normalizeClaudeOAuthRequestBody 时会注入
// temperature/max_tokens/context_management 来 mimic 真实 Claude Code CLI 行为
// （供 /v1/messages 用）。但 count_tokens 端点拒绝这些字段。
// 这个测试验证 ForwardCountTokens 在 normalize 之后再跑一次 sanitize，
// 确保上游收到的 body 不含 normalize 注入的 unsupported 字段。
// 触发场景：客户端 body 不带 temperature、max_tokens、context_management；
// OAuth 账号 + 非 claude-cli UA → mimic 注入 → 最终 sanitize 兜底剥除。
// 没这个兜底，count_tokens 在 OAuth 路径上 100% 返回 400 给客户端
// （生产线 2026-05-18 09:10:30 实测复现）。
func TestGatewayService_ForwardCountTokens_OAuthMimicInjectionGetsStripped(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	// 非 claude-cli UA → 触发 mimic 路径（shouldMimicClaudeCode=true）
	c.Request.Header.Set("User-Agent", "anthropic-sdk-python/0.42.0 python/3.12.0")
	// thinking enabled → normalize 还会注入 context_management（Sonnet/Opus，非 Haiku）
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":2000}}`)
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5", ThinkingEnabled: true}

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":3}`)),
		},
	}

	svc := &GatewayService{
		cfg:                  &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		responseHeaderFilter: compileResponseHeaderFilter(&config.Config{}),
	}

	account := &Account{
		ID:          601,
		Name:        "ct-oauth-mimic-strip",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "oauth-access",
			"refresh_token": "oauth-refresh",
			"expires_at":    "2099-01-01T00:00:00Z",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	err := svc.ForwardCountTokens(context.Background(), c, account, parsed)
	require.NoError(t, err, "count_tokens should succeed (upstream returns 200)")
	require.NotNil(t, upstream.lastBody, "upstream must have received a body")

	// Final sanitize 必须把 normalize 注入的 unsupported 字段全部剥掉。
	require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists(),
		"temperature must be stripped after OAuth mimic injection (Anthropic count_tokens rejects it)")
	require.False(t, gjson.GetBytes(upstream.lastBody, "max_tokens").Exists(),
		"max_tokens must be stripped after OAuth mimic injection")
	require.False(t, gjson.GetBytes(upstream.lastBody, "context_management").Exists(),
		"context_management must be stripped after OAuth mimic injection")

	// 同时确保 supported 字段保留（model + messages + thinking）。
	// model 经 normalize 后可能从短 ID 映射到 long ID（如 claude-sonnet-4-5 →
	// claude-sonnet-4-5-20250929），保留前缀比对即可。
	require.True(t,
		strings.HasPrefix(gjson.GetBytes(upstream.lastBody, "model").String(), "claude-sonnet-4-5"),
		"model should be the (possibly long-id mapped) sonnet 4-5 family")
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray())
	require.Equal(t, "enabled", gjson.GetBytes(upstream.lastBody, "thinking.type").String())
}
