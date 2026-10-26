//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapidto "github.com/QuantumNous/new-api/dto"
	newapitypes "github.com/QuantumNous/new-api/types"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func volcAgentPlanKimiAccountForTest() *Account {
	return &Account{
		ID:          125,
		Name:        "volcengine-agent-plan-test",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "ark-test-key",
			"base_url": newapiintegration.VolcEngineAgentPlanBaseURL,
		},
		// Probe Extra may claim Responses exists (soft 400/HTML). Force-chat
		// must still win for Agent Plan text.
		Extra: map[string]any{
			openai_compat.ExtraKeyResponsesSupported: true,
		},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func TestShouldForwardOpenAIResponsesViaRawChatCompletions_VolcAgentPlanIgnoresResponsesFlag(t *testing.T) {
	account := volcAgentPlanKimiAccountForTest()
	require.True(t, isNewAPIVolcEngineAgentPlanAccount(account))
	require.True(t, shouldForwardOpenAIResponsesViaRawChatCompletions(account),
		"Volc Agent Plan must force Chat Completions even when Extra claims Responses")
}

func TestForwardAsChatCompletions_VolcAgentPlanConvertsResponsesShapedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chatJSON := `{"id":"chatcmpl_k3","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"协议兼容测试通过"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`
	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(chatJSON)),
		},
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := volcAgentPlanKimiAccountForTest()

	// Customer protocol_responses_compat shape posted to chat/completions
	// (ops_error_logs inbound=/v1/chat/completions, missing messages).
	body := []byte(`{
		"model": "kimi-k3",
		"max_output_tokens": 2048,
		"stream": false,
		"store": false,
		"instructions": "请简洁回答。",
		"input": [{"role":"user","content":"只回复：协议兼容测试通过"}]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray(),
		"Responses-shaped chat body must convert to messages before Volc upstream")
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists(),
		"converted chat body must not retain Responses input")
	require.Contains(t, rec.Body.String(), "协议兼容测试通过")
}

func TestForwardAsResponses_VolcAgentPlanUsesChatFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chatJSON := `{"id":"chatcmpl_k3_resp","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(chatJSON)),
		},
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := volcAgentPlanKimiAccountForTest()
	body := []byte(`{"model":"kimi-k3","input":"hello","max_output_tokens":64,"stream":false,"store":false}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", upstream.lastReq.URL.String(),
		"Volc /v1/responses must fall back to chat/completions, not native /responses")
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray())
	require.Equal(t, "response", gjson.Get(rec.Body.String(), "object").String())
}

func TestForwardAsAnthropic_VolcAgentPlanUsesChatFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chatJSON := `{"id":"chatcmpl_k3_msg","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"协议兼容测试通过"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":4,"total_tokens":9}}`
	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(chatJSON)),
		},
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := volcAgentPlanKimiAccountForTest()
	body := []byte(`{"model":"kimi-k3","max_tokens":2048,"stream":false,"messages":[{"role":"user","content":"只回复：协议兼容测试通过"}]}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", upstream.lastReq.URL.String(),
		"Volc /v1/messages must convert via Chat Completions, not HTML Responses")
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray())
	require.Contains(t, rec.Body.String(), "协议兼容测试通过")
}

func TestIsNewAPIResponsesProactiveChatFallbackModel_Kimi(t *testing.T) {
	require.True(t, isNewAPIResponsesProactiveChatFallbackModel("kimi-k3"))
	require.True(t, isNewAPIResponsesProactiveChatFallbackModel("moonshot-v1-128k"))
	require.True(t, isNewAPIResponsesProactiveChatFallbackModel("k3-256k"))
	require.False(t, isNewAPIResponsesProactiveChatFallbackModel("gpt-5"))
}

func TestForwardAsResponsesDispatched_KimiProactiveChatFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldResponses := dispatchNewAPIResponses
	oldChat := dispatchNewAPIChatCompletions
	t.Cleanup(func() {
		dispatchNewAPIResponses = oldResponses
		dispatchNewAPIChatCompletions = oldChat
	})

	responsesCalled := false
	dispatchNewAPIResponses = func(context.Context, *gin.Context, bridge.ChannelContextInput, []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		responsesCalled = true
		return nil, newapitypes.NewError(errors.New("should not call responses adaptor"), newapitypes.ErrorCodeConvertRequestFailed, newapitypes.ErrOptionWithSkipRetry())
	}

	dispatchNewAPIChatCompletions = func(_ context.Context, c *gin.Context, _ bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		require.True(t, gjson.GetBytes(body, "messages").IsArray())
		c.Writer.Header().Set("Content-Type", "application/json")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write([]byte(`{"id":"chatcmpl_kimi","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
		return &bridge.DispatchOutcome{
			Usage:         &newapidto.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
			Model:         "kimi-k3",
			UpstreamModel: "kimi-k3",
			Stream:        false,
		}, nil
	}

	body := []byte(`{"model":"kimi-k3","input":"hello","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	svc := &OpenAIGatewayService{}
	account := &Account{
		ID:          4401,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 43, // non-Volc newapi so bridge owns Responses
		Credentials: map[string]any{"api_key": "sk-newapi", "base_url": "https://newapi.example"},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.ForwardAsResponsesDispatched(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, responsesCalled)
	require.Equal(t, http.StatusOK, rec.Code)
}
