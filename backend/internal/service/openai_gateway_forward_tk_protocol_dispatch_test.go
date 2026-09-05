package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type protocolDispatchHTTPUpstream struct {
	requests []*http.Request
}

func (u *protocolDispatchHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	u.requests = append(u.requests, req)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
		)),
	}, nil
}

func (u *protocolDispatchHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestTkTryRouteOpenAIForwardProtocol_OAuthContinuesNativeResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	svc := &OpenAIGatewayService{}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"model":"gpt-5","input":"hi"}`)

	result, outBody, handled, err := svc.tkTryRouteOpenAIForwardProtocol(context.Background(), c, account, body, "gpt-5")
	require.NoError(t, err)
	require.False(t, handled, "OAuth must fall through to native Responses transport")
	require.Nil(t, result)
	require.Equal(t, body, outBody)
}

func TestTkTryRouteOpenAIForwardProtocol_APIKeyNormalizeMutatesOutBodyThenContinues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	svc := &OpenAIGatewayService{}
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "sk-test",
			"base_url": "https://api.openai.com",
		},
		// Extra empty → ShouldUseResponsesAPI true → shouldForward false.
		Extra: map[string]any{},
	}
	body := []byte(`{"model":"gpt-5","input":"hi","parallel_tool_calls":true}`)

	result, outBody, handled, err := svc.tkTryRouteOpenAIForwardProtocol(context.Background(), c, account, body, "gpt-5")
	require.NoError(t, err)
	require.False(t, handled, "after normalize, native Responses must continue when Extra allows Responses")
	require.Nil(t, result)
	require.NotEqual(t, string(body), string(outBody), "normalize must strip parallel_tool_calls without tools")
	require.NotContains(t, string(outBody), "parallel_tool_calls")
}

func TestTkTryRouteOpenAIForwardProtocol_CloudwiseRawChatAfterNormalize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &protocolDispatchHTTPUpstream{}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:           false,
			AllowInsecureHTTP: true,
		}}},
		httpUpstream: upstream,
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(nil))

	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":  "sk-cloudwise",
			"base_url": "https://api.cloudwise.ai/api",
		},
	}
	require.True(t, account.IsOpenAICloudwiseRelay())
	body := []byte(`{"model":"gpt-5","input":"hi","parallel_tool_calls":true}`)

	result, outBody, handled, err := svc.tkTryRouteOpenAIForwardProtocol(context.Background(), c, account, body, "gpt-5")
	require.NoError(t, err)
	require.True(t, handled, "cloudwise dual-stack must route via raw Chat Completions")
	require.NotNil(t, result)
	require.NotContains(t, string(outBody), "parallel_tool_calls", "normalize must run BEFORE shouldForward raw-CC")
	require.NotEmpty(t, upstream.requests, "raw-CC path must hit upstream")
	require.Contains(t, upstream.requests[0].URL.Path, "chat/completions")
}

func TestTkTryRouteOpenAIForwardProtocol_AnthropicProtocolBeforeRawChat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &protocolDispatchHTTPUpstream{}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:           false,
			AllowInsecureHTTP: true,
		}}},
		httpUpstream: upstream,
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	account := &Account{
		Platform: PlatformKimi,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":      "sk-kimi",
			"base_url":     "http://kimi.example",
			"api_protocol": APIProtocolAnthropic,
		},
	}
	require.True(t, account.IsAnthropicProtocol())
	require.True(t, account.IsCNProvider())

	body := []byte(`{"model":"kimi-k2","input":"hi"}`)
	_, _, handled, _ := svc.tkTryRouteOpenAIForwardProtocol(context.Background(), c, account, body, "kimi-k2")
	// Native Anthropic conversion may error without full fixtures; what matters is
	// handled=true (Anthropic branch taken) and NOT chat/completions upstream.
	require.True(t, handled, "Anthropic protocol must short-circuit before raw-CC")
	for _, req := range upstream.requests {
		require.NotContains(t, req.URL.Path, "chat/completions", "must not fall through to raw-CC URL construction")
	}
}

func TestTkTryRouteOpenAIForwardProtocol_UnsupportedPlannedTargetErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	// ChatCompletions→Gemini is a legal plan target that Forward's companion
	// switch does not implement (defensive default arm).
	account := &Account{
		ID:       902,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "ag-token",
			"project_id":    "ag-project",
			"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"},
		},
		Extra: map[string]any{SupportedProtocolsExtraKey: []any{string(protocolrouter.ProtocolGeminiGenerateContent)}},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolGeminiGenerateContent)
	snapshot, err := ProtocolAccountSnapshot(account, "gemini-2.5-flash")
	require.NoError(t, err)
	req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolChatCompletions,
		RequestedModel:  "gemini-2.5-flash",
		Profile:         protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
		Body:            []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`),
	})
	require.NoError(t, err)
	plan, err := NewProtocolRouter().Plan(req, snapshot)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, plan.TargetProtocol())

	ctx := withProtocolExecutionPlan(context.Background(), plan)
	svc := &OpenAIGatewayService{}

	result, _, handled, routeErr := svc.tkTryRouteOpenAIForwardProtocol(ctx, c, account, []byte(`{}`), "gemini-2.5-flash")
	require.True(t, handled)
	require.Nil(t, result)
	require.Error(t, routeErr)
	require.Contains(t, routeErr.Error(), "unsupported selected protocol target")
}
