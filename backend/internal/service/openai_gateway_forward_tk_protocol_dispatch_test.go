package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

	result, outBody, handled, err := svc.tkTryRouteOpenAIForwardProtocol(context.Background(), c, account, body, "gpt-5", time.Time{})
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

	result, outBody, handled, err := svc.tkTryRouteOpenAIForwardProtocol(context.Background(), c, account, body, "gpt-5", time.Time{})
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

	result, outBody, handled, err := svc.tkTryRouteOpenAIForwardProtocol(context.Background(), c, account, body, "gpt-5", time.Time{})
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
	_, _, handled, _ := svc.tkTryRouteOpenAIForwardProtocol(context.Background(), c, account, body, "kimi-k2", time.Time{})
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

	result, _, handled, routeErr := svc.tkTryRouteOpenAIForwardProtocol(ctx, c, account, []byte(`{}`), "gemini-2.5-flash", time.Time{})
	require.True(t, handled)
	require.Nil(t, result)
	require.Error(t, routeErr)
	require.Contains(t, routeErr.Error(), "unsupported selected protocol target")
}

func TestForwardPreservesSelectedResponsesPlanOverProviderRules(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &Account{
		ID: 903, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "test-key", "base_url": "https://provider.example",
			"model_mapping": map[string]any{"deepseek-v4": "deepseek-v4"},
		},
		Extra: map[string]any{
			"openai_responses_supported": false,
		},
	}
	attachTestProtocolCapability(account, protocolrouter.ProtocolResponses)
	body := []byte(`{"model":"deepseek-v4","input":"hi","stream":false}`)
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolResponses, RequestedModel: "deepseek-v4",
		Body: body,
	})
	require.NoError(t, err)
	snapshot, err := protocolAccountSnapshotForRequest(account, request)
	require.NoError(t, err)
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.ProtocolResponses, plan.TargetProtocol())
	require.Equal(t, APIProtocolChatCompletions, resolveUpstreamProtocol(account, APIProtocolResponses, "deepseek-v4", nil))

	upstream := &protocolDispatchHTTPUpstream{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	result, err := svc.Forward(withProtocolExecutionPlan(context.Background(), plan), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "/v1/responses", upstream.requests[0].URL.Path,
		"provider defaults must not replace the scheduler's immutable protocol plan")
	sentBody, err := io.ReadAll(upstream.requests[0].Body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(sentBody, "messages").Exists(), "Responses endpoint must not receive a Chat body")
	require.Equal(t, "hi", gjson.GetBytes(sentBody, "input").String())
}

func TestChatResponsesRejectionDoesNotReplaceSelectedPlan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := structuredInputRetryTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.4"}
	attachTestProtocolCapability(account, protocolrouter.ProtocolResponses)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathRoot, "gpt-5.4", false, body)
	require.NoError(t, err)
	snapshot, err := protocolAccountSnapshotForRequest(account, request)
	require.NoError(t, err)
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.ProtocolResponses, plan.TargetProtocol())

	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"invalid_type","param":"input","message":"Expected a string for input, but got an array instead."}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_1","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)),
		},
	}}
	svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), httpUpstream: upstream}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	_, err = svc.ForwardAsChatCompletions(withProtocolExecutionPlan(context.Background(), plan), c, account, body, "", "")
	require.Len(t, upstream.requests, 1, "an error must not send to an unplanned, unverified endpoint")
	require.Error(t, err)
	require.Equal(t, "/v1/responses", upstream.requests[0].URL.Path)
}
