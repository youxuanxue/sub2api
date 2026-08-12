//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func tokenseaAnthropicRelayAccount() *Account {
	account := newAnthropicAPIKeyAccountForTest()
	account.Credentials["base_url"] = "https://agent.tokensea.ai"
	account.Credentials["model_mapping"] = modelMappingToAny(anthropicTokenseaRelayModelMappingFloor())
	return account
}

func TestAnthropicTokenseaRelayFloorMapsShortNamesToWireIDs(t *testing.T) {
	t.Parallel()
	mapping := anthropicTokenseaRelayModelMappingFloor()
	require.Equal(t, "claude-haiku-4-5-20251001", mapping["claude-haiku-4-5"])
	require.Equal(t, "claude-opus-4-5-20251101", mapping["claude-opus-4-5"])
	require.Equal(t, "claude-opus-5", mapping["claude-opus-5"])
	require.Len(t, mapping, len(supportedAnthropicTokenseaRelayCatalogModels))
}

func TestBuildAnthropicCompatUpstreamRequest_PassthroughUsesTokenseaMessagesURL(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-6","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))),
		},
	}
	account := tokenseaAnthropicRelayAccount()

	svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	req, _, err := svc.buildAnthropicCompatUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"claude-sonnet-4-6","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`),
		"upstream-key", "apikey", "claude-sonnet-4-6", false, false,
	)
	require.NoError(t, err)
	require.NotNil(t, req)
	require.Equal(t, "https://agent.tokensea.ai/v1/messages?beta=true", req.URL.String())
	require.Equal(t, "upstream-key", getHeaderRaw(req.Header, "x-api-key"))
}

func TestForwardAsChatCompletions_AnthropicTokenseaPassthroughMapsHaikuWireID(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(bytes.NewReader([]byte(`{
				"type":"message",
				"role":"assistant",
				"content":[{"type":"text","text":"ok"}],
				"model":"claude-haiku-4-5-20251001",
				"stop_reason":"end_turn",
				"usage":{"input_tokens":1,"output_tokens":1}
			}`))),
		},
	}
	account := tokenseaAnthropicRelayAccount()
	body := []byte(`{"model":"claude-haiku-4-5","stream":false,"messages":[{"role":"user","content":"hi"}],"max_tokens":8}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	svc := &GatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}
	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
	require.NoError(t, err)
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "https://agent.tokensea.ai/v1/messages?beta=true", upstream.lastReq.URL.String())
}

func TestBuildAnthropicCompatUpstreamRequest_OAuthPassthroughUsesBearerAuth(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-sonnet-4-6","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))),
		},
	}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "oauth-upstream-token",
		},
		Extra: map[string]any{
			"anthropic_oauth_passthrough": true,
		},
	}

	svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	req, _, err := svc.buildAnthropicCompatUpstreamRequest(
		context.Background(), c, account,
		[]byte(`{"model":"claude-sonnet-4-6","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`),
		"oauth-upstream-token", "oauth", "claude-sonnet-4-6", false, false,
	)
	require.NoError(t, err)
	require.NotNil(t, req)
	require.Equal(t, "Bearer oauth-upstream-token", getHeaderRaw(req.Header, "authorization"))
	require.Empty(t, getHeaderRaw(req.Header, "x-api-key"))
}
