package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_StripsTimeoutHeadersByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("x-stainless-timeout", "120000")
	c.Request.Header.Set("x-stainless-read-timeout", "120000")
	c.Request.Header.Set("user-agent", "claude-cli/1.0.0")

	svc := &GatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	account := newAnthropicAPIKeyAccountForTest()

	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, account,
		[]byte(`{"model":"claude-sonnet-4-20250514","messages":[]}`),
		"k",
	)
	require.NoError(t, err)
	require.Empty(t, getHeaderRaw(req.Header, "x-stainless-timeout"))
	require.Empty(t, getHeaderRaw(req.Header, "x-stainless-read-timeout"))
	require.Equal(t, "claude-cli/1.0.0", getHeaderRaw(req.Header, "user-agent"))
}

func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_AllowsTimeoutHeadersWhenConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("x-stainless-timeout", "120000")

	svc := &GatewayService{
		cfg: &config.Config{
			Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}},
			Gateway: config.GatewayConfig{
				AnthropicPassthroughAllowTimeoutHeaders: true,
			},
		},
	}
	account := newAnthropicAPIKeyAccountForTest()

	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, account,
		[]byte(`{"model":"claude-sonnet-4-20250514","messages":[]}`),
		"k",
	)
	require.NoError(t, err)
	require.Equal(t, "120000", getHeaderRaw(req.Header, "x-stainless-timeout"))
}

func TestBuildCountTokensRequestAnthropicAPIKeyPassthrough_StripsTimeoutHeadersByDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("x-stainless-timeout", "120000")

	svc := &GatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	account := newAnthropicAPIKeyAccountForTest()

	req, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, account,
		[]byte(`{"model":"claude-sonnet-4-20250514","messages":[]}`),
		"k",
	)
	require.NoError(t, err)
	require.Empty(t, getHeaderRaw(req.Header, "x-stainless-timeout"))
}
