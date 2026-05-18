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
	parsed := &ParsedRequest{Body: body, Model: "claude-opus-4-7"}

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

// TestGatewayService_ForwardCountTokens_429StillTripsUpstreamErrorBreaker
// 反向回归：count_tokens 端的 429（真实容量信号）仍然应该计入 breaker。
// 这保证 #3 修复只豁免 400，不破坏正常的容量保护。
func TestGatewayService_ForwardCountTokens_429StillTripsUpstreamErrorBreaker(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}]}`)
	parsed := &ParsedRequest{Body: body, Model: "claude-opus-4-7"}

	upstreamRespBody := `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"}}`
	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusTooManyRequests,
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
		Name:        "ct-429-still-counts",
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

	_ = svc.ForwardCountTokens(context.Background(), c, account, parsed)
	require.Equal(t, []int64{502}, counter.incrementIDs, "count_tokens 429 must still feed the anthropic_upstream_error counter (capacity signal)")
}
