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
	"github.com/tidwall/gjson"
)

func newAnthropicOAuthAccountForPassthroughTest() *Account {
	return &Account{
		ID:          301,
		Name:        "anthropic-oauth-pass-test",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "upstream-oauth-token",
		},
		Extra: map[string]any{
			"anthropic_oauth_passthrough": true,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func TestBuildUpstreamRequestAnthropicOAuthPassthrough_PreservesClientHeadersAndBearerAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.0")
	c.Request.Header.Set("Authorization", "Bearer inbound-token")
	c.Request.Header.Set("X-Api-Key", "inbound-api-key")
	c.Request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")
	c.Request.Header.Set("X-Stainless-Retry-Count", "3")

	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
	account := newAnthropicOAuthAccountForPassthroughTest()

	svc := &GatewayService{
		cfg: &config.Config{},
	}

	req, wireBody, err := svc.buildUpstreamRequestAnthropicOAuthPassthrough(
		context.Background(), c, account, body, "upstream-oauth-token",
	)
	require.NoError(t, err)
	require.Equal(t, body, wireBody)
	require.Equal(t, "Bearer upstream-oauth-token", getHeaderRaw(req.Header, "authorization"))
	require.Empty(t, getHeaderRaw(req.Header, "x-api-key"))
	require.Equal(t, "claude-cli/2.1.0", getHeaderRaw(req.Header, "user-agent"))
	require.Equal(t, "interleaved-thinking-2025-05-14", getHeaderRaw(req.Header, "anthropic-beta"))
	require.Equal(t, "3", getHeaderRaw(req.Header, "x-stainless-retry-count"))
}

func TestGatewayService_AnthropicOAuthPassthrough_ForwardSkipsFingerprintRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "python-requests/2.32")
	c.Request.Header.Set("Anthropic-Beta", "oauth-2025-04-20")

	body := []byte(`{"model":"claude-sonnet-4-6","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed := &ParsedRequest{
		Body:   NewRequestBodyRef(body),
		Model:  "claude-sonnet-4-6",
		Stream: false,
	}

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"x-request-id": []string{"rid-oauth-pass"},
			},
			Body: io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-6","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)),
		},
	}

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
	}

	account := newAnthropicOAuthAccountForPassthroughTest()
	_, err := svc.forwardAnthropicOAuthPassthroughWithInput(context.Background(), c, account, anthropicPassthroughForwardInput{
		Body:          body,
		Parsed:        parsed,
		RequestModel:  "claude-sonnet-4-6",
		OriginalModel: "claude-sonnet-4-6",
		RequestStream: false,
		StartTime:     time.Now(),
	})
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "python-requests/2.32", getHeaderRaw(upstream.lastReq.Header, "user-agent"))
	require.Equal(t, "Bearer upstream-oauth-token", getHeaderRaw(upstream.lastReq.Header, "authorization"))
	require.Empty(t, getHeaderRaw(upstream.lastReq.Header, "x-stainless-lang"), "OAuth 透传不应注入 OAuth 指纹头")
	require.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "messages.0.content.0.text").String())
}
