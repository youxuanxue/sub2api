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
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
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

// TestForward_VolcForceChat_BoundResponsesIdentityPlanPostsToChatURL guards the
// production path: ExecuteSelectedProtocol binds ResponsesIdentity with a
// /responses endpoint. Force-chat must convert the body AND still POST to
// /chat/completions — posting messages to /responses yields missing `input`
// (cn-prod 4cb4479d / responses_compat after 1.8.326).
func TestForward_VolcForceChat_BoundResponsesIdentityPlanPostsToChatURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chatJSON := `{"id":"chatcmpl_k3_plan","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(chatJSON)),
		},
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := volcAgentPlanKimiAccountForTest()
	body := []byte(`{"model":"kimi-k3","input":[{"role":"user","content":"hi"}],"max_output_tokens":64,"stream":false,"store":false,"instructions":"请简洁回答。"}`)

	responsesURL := newapiintegration.VolcEngineAgentPlanBaseURL + "/responses"
	chatURL := newapiintegration.VolcEngineAgentPlanBaseURL + "/chat/completions"
	ctx := withProtocolExecutionPlan(context.Background(), volcAgentPlanResponsesIdentityPlan(t, body, responsesURL, chatURL))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	result, err := svc.Forward(ctx, c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, chatURL, upstream.lastReq.URL.String(),
		"bound ResponsesIdentity plan must not pin force-chat to /responses")
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray())
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.Equal(t, "response", gjson.Get(rec.Body.String(), "object").String())
}

// TestForwardAsChatCompletions_BoundChatPlanConvertsResponsesShapedBody guards
// the early protocolExecutionTarget ChatCompletions return: without convert,
// Responses-shaped bodies hit Volc as missing `messages` (cn-prod 45bdb549).
func TestForwardAsChatCompletions_BoundChatPlanConvertsResponsesShapedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chatJSON := `{"id":"chatcmpl_k3_shape","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"协议兼容测试通过"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`
	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(chatJSON)),
		},
	}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := volcAgentPlanKimiAccountForTest()
	body := []byte(`{
		"model": "kimi-k3",
		"max_output_tokens": 2048,
		"stream": false,
		"store": false,
		"instructions": "请简洁回答。",
		"input": [{"role":"user","content":"只回复：协议兼容测试通过"}]
	}`)

	chatURL := newapiintegration.VolcEngineAgentPlanBaseURL + "/chat/completions"
	ctx := withProtocolExecutionPlan(context.Background(), volcAgentPlanChatIdentityPlan(t, body, chatURL))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	result, err := svc.ForwardAsChatCompletions(ctx, c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, chatURL, upstream.lastReq.URL.String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "messages").IsArray(),
		"bound Chat plan must still convert Responses-shaped chat body")
	require.False(t, gjson.GetBytes(upstream.lastBody, "input").Exists())
	require.Contains(t, rec.Body.String(), "协议兼容测试通过")
}

// TestVolcAgentPlan_ResponsesInboundPlansToChat verifies chat-only Plan:
// capability without Responses, and stale Responses rows, both select
// responses_to_chat (not ResponsesIdentity), including tools/image shapes.
func TestVolcAgentPlan_ResponsesInboundPlansToChat(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capability []protocolrouter.Protocol
		profile    protocolrouter.RequestProfile
	}{
		{
			name:       "chat_only_text",
			capability: []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions},
			profile:    protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
		},
		{
			name: "stale_responses_row_tools_image",
			capability: []protocolrouter.Protocol{
				protocolrouter.ProtocolChatCompletions,
				protocolrouter.ProtocolResponses,
			},
			profile: protocolrouter.RequestProfile{
				Tools:        true,
				ToolChoice:   protocolrouter.ToolChoiceAuto,
				ContentKinds: protocolrouter.ContentText | protocolrouter.ContentImage,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := volcAgentPlanKimiAccountForTest()
			account.Credentials["model_mapping"] = map[string]any{"kimi-k3": "kimi-k3"}
			attachTestProtocolCapability(account, tc.capability...)

			body := []byte(`{"model":"kimi-k3","input":[{"role":"user","content":"hi"}],"stream":false}`)
			req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
				InboundProtocol: protocolrouter.ProtocolResponses,
				RequestedModel:  "kimi-k3",
				ResponsesPath:   protocolrouter.ResponsesPathRoot,
				Profile:         tc.profile,
				Body:            body,
			})
			require.NoError(t, err)

			snap, err := protocolAccountSnapshotForRequest(account, req)
			require.NoError(t, err)
			plan, err := protocolRoutingTestRouter().Plan(req, snap)
			require.NoError(t, err)
			require.Equal(t, protocolrouter.ProtocolChatCompletions, plan.TargetProtocol(),
				"Agent Plan must not plan ResponsesIdentity")
			require.Equal(t, protocolrouter.AdapterResponsesToChat, plan.AdapterID())
			require.Equal(t, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", plan.Endpoint())
		})
	}
}

func volcAgentPlanResponsesIdentityPlan(t *testing.T, body []byte, responsesURL, chatURL string) protocolrouter.Plan {
	t.Helper()
	req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolResponses,
		RequestedModel:  "kimi-k3",
		ResponsesPath:   protocolrouter.ResponsesPathRoot,
		Profile:         protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
		Body:            body,
	})
	require.NoError(t, err)
	snap, err := protocolrouter.NewAccountSnapshot(protocolrouter.AccountSnapshotInput{
		AccountID:          125,
		CapabilityKey:      "volc-agent-plan",
		SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolResponses, protocolrouter.ProtocolChatCompletions},
		ResolvedModel:      "kimi-k3",
		ExactEndpoints: map[protocolrouter.Protocol]string{
			protocolrouter.ProtocolResponses:       responsesURL,
			protocolrouter.ProtocolChatCompletions: chatURL,
		},
		ModelAllowed: map[protocolrouter.Protocol]bool{
			protocolrouter.ProtocolResponses:       true,
			protocolrouter.ProtocolChatCompletions: true,
		},
		Transports: []protocolrouter.TransportID{protocolrouter.TransportHTTP},
	})
	require.NoError(t, err)
	plan, err := protocolRoutingTestRouter().Plan(req, snap)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.ProtocolResponses, plan.TargetProtocol())
	require.Equal(t, responsesURL, plan.Endpoint())
	return plan
}

func volcAgentPlanChatIdentityPlan(t *testing.T, body []byte, chatURL string) protocolrouter.Plan {
	t.Helper()
	req, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: protocolrouter.ProtocolChatCompletions,
		RequestedModel:  "kimi-k3",
		Profile:         protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText},
		Body:            body,
	})
	require.NoError(t, err)
	snap, err := protocolrouter.NewAccountSnapshot(protocolrouter.AccountSnapshotInput{
		AccountID:          125,
		CapabilityKey:      "volc-agent-plan",
		SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolChatCompletions},
		ResolvedModel:      "kimi-k3",
		ExactEndpoints: map[protocolrouter.Protocol]string{
			protocolrouter.ProtocolChatCompletions: chatURL,
		},
		ModelAllowed: map[protocolrouter.Protocol]bool{protocolrouter.ProtocolChatCompletions: true},
		Transports:   []protocolrouter.TransportID{protocolrouter.TransportHTTP},
	})
	require.NoError(t, err)
	plan, err := protocolRoutingTestRouter().Plan(req, snap)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.ProtocolChatCompletions, plan.TargetProtocol())
	require.Equal(t, chatURL, plan.Endpoint())
	return plan
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

func TestForwardAsChatCompletionsDispatched_ConvertsResponsesShapedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })

	var capturedBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, c *gin.Context, _ bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		capturedBody = append([]byte(nil), body...)
		c.Writer.Header().Set("Content-Type", "application/json")
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.Write([]byte(`{"id":"chatcmpl_bridge","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`))
		return &bridge.DispatchOutcome{
			Usage:         &newapidto.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4},
			Model:         "kimi-k3",
			UpstreamModel: "kimi-k3",
			Stream:        false,
		}, nil
	}

	account := &Account{
		ID:          4501,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 43,
		Credentials: map[string]any{"api_key": "sk-newapi", "base_url": "https://newapi.example"},
		Status:      StatusActive,
		Schedulable: true,
	}
	body := []byte(`{
		"model": "kimi-k3",
		"stream": false,
		"instructions": "请简洁回答。",
		"input": [{"role":"user","content":"hello"}]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	result, err := (&OpenAIGatewayService{}).ForwardAsChatCompletionsDispatched(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, gjson.GetBytes(capturedBody, "messages").IsArray(),
		"bridge chat dispatch must convert Responses-shaped body to messages")
	require.False(t, gjson.GetBytes(capturedBody, "input").Exists(),
		"converted bridge chat body must not retain Responses input")
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
