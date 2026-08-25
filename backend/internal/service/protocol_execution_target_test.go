package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type protocolTargetHTTPUpstream struct {
	requests  []*http.Request
	responses []*http.Response
}

func (u *protocolTargetHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	u.requests = append(u.requests, req)
	if len(u.responses) == 0 {
		return nil, io.ErrUnexpectedEOF
	}
	resp := u.responses[0]
	u.responses = u.responses[1:]
	return resp, nil
}

func (u *protocolTargetHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func protocolTargetTestService(upstream *protocolTargetHTTPUpstream) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:           false,
			AllowInsecureHTTP: true,
		}}},
		httpUpstream: upstream,
	}
}

func protocolTargetTestAccount(protocols ...protocolrouter.Protocol) *Account {
	values := make([]any, len(protocols))
	for i, protocol := range protocols {
		values[i] = string(protocol)
	}
	return &Account{
		ID:          901,
		Name:        "protocol-target",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "sk-protocol-target",
			"base_url": "http://upstream.example",
		},
		Extra: map[string]any{SupportedProtocolsExtraKey: values},
	}
}

func protocolTargetTestExecution(
	t *testing.T,
	inbound protocolrouter.Protocol,
	body []byte,
	account *Account,
	execute ProtocolExecutionFunc,
) (any, error) {
	t.Helper()
	requestedModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{
		InboundProtocol: inbound,
		RequestedModel:  requestedModel,
		Profile: protocolrouter.RequestProfile{
			ContentKinds: protocolrouter.ContentText,
		},
		Body: body,
	})
	if err != nil {
		t.Fatalf("NewCanonicalRequest: %v", err)
	}
	router := NewProtocolRouter()
	ctx := WithProtocolRouting(context.Background(), router, request)
	plan, _, err := protocolPlanForAccount(ctx, account, requestedModel)
	if err != nil {
		t.Fatalf("protocolPlanForAccount: %v", err)
	}
	return ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{
		Account:      account,
		ProtocolPlan: &plan,
	}, account, func(context.Context, *Account, string) error { return nil }, protocolExecutorsForTest(plan, execute))
}

func TestProtocolExecutionPlanOverridesLegacyChatRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		)),
	}}}
	svc := protocolTargetTestService(upstream)
	account := protocolTargetTestAccount(protocolrouter.ProtocolChatCompletions)
	account.Extra[openai_compat.ExtraKeyResponsesSupported] = true

	_, err := protocolTargetTestExecution(t, protocolrouter.ProtocolChatCompletions, body, account, func(
		executionCtx context.Context,
		_ protocolrouter.Plan,
		_ protocolrouter.CanonicalRequest,
	) (any, error) {
		return svc.ForwardAsChatCompletions(executionCtx, c, account, body, "", "")
	})
	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	if len(upstream.requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(upstream.requests))
	}
	if got := upstream.requests[0].URL.String(); got != "http://upstream.example/v1/chat/completions" {
		t.Fatalf("upstream URL = %q, want plan-selected chat_completions endpoint", got)
	}
}

func TestProtocolExecutionPlanOverridesLegacyMessagesRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-client","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	legacyBody := []byte(`{"model":"claude-legacy","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"type":"message","model":"gpt-5.4","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"output_tokens":1}}`,
		)),
	}}}
	svc := protocolTargetTestService(upstream)
	account := protocolTargetTestAccount(protocolrouter.ProtocolMessages)
	account.Credentials["model_mapping"] = map[string]any{"claude-client": "claude-wire"}
	account.Credentials["api_base_urls"] = map[string]any{
		APIProtocolAnthropic: "http://messages-specific.example/custom",
	}
	account.Extra[openai_compat.ExtraKeyResponsesSupported] = true
	account.Extra[openai_compat.ExtraKeyNativeMessagesSupported] = false

	_, err := protocolTargetTestExecution(t, protocolrouter.ProtocolMessages, body, account, func(
		executionCtx context.Context,
		_ protocolrouter.Plan,
		_ protocolrouter.CanonicalRequest,
	) (any, error) {
		return svc.ForwardAsAnthropic(executionCtx, c, account, legacyBody, "", "")
	})
	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	if len(upstream.requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(upstream.requests))
	}
	if got := upstream.requests[0].URL.String(); got != "http://messages-specific.example/custom/v1/messages" {
		t.Fatalf("upstream URL = %q, want plan-selected messages endpoint", got)
	}
	if got := gjson.GetBytes(readProtocolTargetRequestBody(t, upstream.requests[0]), "model").String(); got != "claude-wire" {
		t.Fatalf("upstream model = %q, want plan-resolved model", got)
	}
}

func TestProtocolMessagesIdentityUsesNativeAnthropicCredentialContractForCNAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-client","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{protocolTargetAnthropicBufferedResponse()}}
	svc := protocolTargetTestService(upstream)
	account := protocolTargetTestAccount(protocolrouter.ProtocolMessages)
	account.Platform = PlatformKimi
	account.Credentials["api_protocol"] = APIProtocolAdaptive
	account.Credentials["model_mapping"] = map[string]any{"claude-client": "claude-sonnet-4-6"}
	account.Credentials["api_base_urls"] = map[string]any{
		APIProtocolAnthropic: "http://upstream.example",
	}

	_, err := protocolTargetTestExecution(t, protocolrouter.ProtocolMessages, body, account, func(
		executionCtx context.Context,
		_ protocolrouter.Plan,
		request protocolrouter.CanonicalRequest,
	) (any, error) {
		return svc.ForwardAsAnthropic(executionCtx, c, account, request.Body(), "", "")
	})
	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	if len(upstream.requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(upstream.requests))
	}
	request := upstream.requests[0]
	if got := request.Header.Get("x-api-key"); got != "sk-protocol-target" {
		t.Fatalf("x-api-key = %q, want native Anthropic credential", got)
	}
	if got := request.Header.Get("Authorization"); got != "" {
		t.Fatalf("authorization = %q, want no OpenAI bearer credential", got)
	}
}

func TestProtocolExecutionDoesNotFallbackProtocolOnSameAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	legacyBody := []byte(`{"model":"legacy-remapped-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"not found"}}`)),
	}}}
	svc := protocolTargetTestService(upstream)
	account := protocolTargetTestAccount(protocolrouter.ProtocolResponses)
	account.Credentials["model_mapping"] = map[string]any{"client-model": "wire-model"}
	account.Credentials["api_base_urls"] = map[string]any{
		APIProtocolResponses: "http://responses-specific.example/custom",
	}

	_, err := protocolTargetTestExecution(t, protocolrouter.ProtocolChatCompletions, body, account, func(
		executionCtx context.Context,
		_ protocolrouter.Plan,
		_ protocolrouter.CanonicalRequest,
	) (any, error) {
		return svc.ForwardAsChatCompletions(executionCtx, c, account, legacyBody, "", "")
	})
	if err == nil {
		t.Fatal("ExecuteSelectedProtocol error = nil, want upstream failure")
	}
	if len(upstream.requests) != 1 {
		t.Fatalf("upstream requests = %d, want one plan-selected attempt without protocol fallback", len(upstream.requests))
	}
	if got := upstream.requests[0].URL.String(); got != "http://responses-specific.example/custom/v1/responses" {
		t.Fatalf("upstream URL = %q, want plan-selected responses endpoint", got)
	}
	if got := gjson.GetBytes(readProtocolTargetRequestBody(t, upstream.requests[0]), "model").String(); got != "wire-model" {
		t.Fatalf("upstream model = %q, want plan-resolved model", got)
	}
}

func TestProtocolExecutionNewAPIResponsesUsesPlannedEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{
		protocolRouteContractResponse(protocolRouteSpecByAdapter(t, protocolrouter.AdapterChatToResponses)),
	}}
	svc := protocolTargetTestService(upstream)
	account := protocolTargetTestAccount(protocolrouter.ProtocolResponses)
	account.Platform = PlatformNewAPI
	account.ChannelType = newapiconstant.ChannelTypeOpenAI
	account.Credentials["model_mapping"] = map[string]any{"client-model": "wire-model"}

	_, err := protocolTargetTestExecution(t, protocolrouter.ProtocolChatCompletions, body, account, func(
		executionCtx context.Context,
		plan protocolrouter.Plan,
		_ protocolrouter.CanonicalRequest,
	) (any, error) {
		if strings.Contains(plan.Endpoint(), "api.openai.com") {
			t.Fatalf("newapi plan endpoint = %q, must not use official OpenAI host", plan.Endpoint())
		}
		return svc.ForwardAsChatCompletions(executionCtx, c, account, body, "", "")
	})
	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	if len(upstream.requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(upstream.requests))
	}
	if got := upstream.requests[0].URL.String(); got != "http://upstream.example/v1/responses" {
		t.Fatalf("upstream URL = %q, want newapi plan-selected Responses endpoint", got)
	}
}

func protocolRouteSpecByAdapter(t *testing.T, adapterID protocolrouter.RouteAdapterID) protocolrouter.RouteSpec {
	t.Helper()
	for _, route := range protocolrouter.RouteSpecs() {
		if route.AdapterID() == adapterID {
			return route
		}
	}
	t.Fatalf("route adapter %q not found", adapterID)
	return protocolrouter.RouteSpec{}
}

func TestProtocolExecutionBindsPlanResolvedModelToUpstreamRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	canonicalBody := []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	legacyBody := []byte(`{"model":"legacy-remapped-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(canonicalBody))
	upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_model","object":"chat.completion","model":"wire-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		)),
	}}}
	svc := protocolTargetTestService(upstream)
	account := protocolTargetTestAccount(protocolrouter.ProtocolChatCompletions)
	account.Credentials["model_mapping"] = map[string]any{"client-model": "wire-model"}

	_, err := protocolTargetTestExecution(t, protocolrouter.ProtocolChatCompletions, canonicalBody, account, func(
		executionCtx context.Context,
		_ protocolrouter.Plan,
		_ protocolrouter.CanonicalRequest,
	) (any, error) {
		return svc.ForwardAsChatCompletions(executionCtx, c, account, legacyBody, "", "")
	})
	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	if len(upstream.requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(upstream.requests))
	}
	if got := gjson.GetBytes(readProtocolTargetRequestBody(t, upstream.requests[0]), "model").String(); got != "wire-model" {
		t.Fatalf("upstream model = %q, want plan-resolved model", got)
	}
}

func TestProtocolExecutionBindsPlanEndpointToUpstreamRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_endpoint","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		)),
	}}}
	svc := protocolTargetTestService(upstream)
	account := protocolTargetTestAccount(protocolrouter.ProtocolChatCompletions)
	account.Credentials["api_base_urls"] = map[string]any{
		APIProtocolChatCompletions: "http://protocol-specific.example/custom",
	}

	_, err := protocolTargetTestExecution(t, protocolrouter.ProtocolChatCompletions, body, account, func(
		executionCtx context.Context,
		_ protocolrouter.Plan,
		_ protocolrouter.CanonicalRequest,
	) (any, error) {
		return svc.ForwardAsChatCompletions(executionCtx, c, account, body, "", "")
	})
	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	if len(upstream.requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(upstream.requests))
	}
	if got := upstream.requests[0].URL.String(); got != "http://protocol-specific.example/custom/v1/chat/completions" {
		t.Fatalf("upstream URL = %q, want exact plan-selected endpoint", got)
	}
}

func TestProtocolExecutionBindsNewAPIBridgeToPlanModelAndEndpoint(t *testing.T) {
	oldDispatch := dispatchNewAPIChatCompletions
	t.Cleanup(func() { dispatchNewAPIChatCompletions = oldDispatch })
	var capturedInput bridge.ChannelContextInput
	var capturedBody []byte
	dispatchNewAPIChatCompletions = func(_ context.Context, _ *gin.Context, in bridge.ChannelContextInput, body []byte) (*bridge.DispatchOutcome, *newapitypes.NewAPIError) {
		capturedInput = in
		capturedBody = append([]byte(nil), body...)
		return &bridge.DispatchOutcome{Model: "wire-model"}, nil
	}

	body := []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	account := protocolTargetTestAccount(protocolrouter.ProtocolChatCompletions)
	account.Platform = PlatformNewAPI
	account.ChannelType = newapiconstant.ChannelTypeOpenAI
	account.Credentials["model_mapping"] = map[string]any{"client-model": "wire-model"}

	_, err := protocolTargetTestExecution(t, protocolrouter.ProtocolChatCompletions, body, account, func(
		executionCtx context.Context,
		_ protocolrouter.Plan,
		request protocolrouter.CanonicalRequest,
	) (any, error) {
		return (&OpenAIGatewayService{}).ForwardAsChatCompletionsDispatched(executionCtx, c, account, request.Body(), "", "")
	})
	if err != nil {
		t.Fatalf("ExecuteSelectedProtocol: %v", err)
	}
	if got := gjson.GetBytes(capturedBody, "model").String(); got != "wire-model" {
		t.Fatalf("bridge model = %q, want plan-resolved wire-model", got)
	}
	if capturedInput.ModelMappingJSON != "" {
		t.Fatalf("bridge model mapping = %q, want disabled after plan resolution", capturedInput.ModelMappingJSON)
	}
}

func TestProtocolRouteRegistryRealAdaptersHonorWireContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range protocolrouter.RouteSpecs() {
		route := route
		t.Run(string(route.AdapterID()), func(t *testing.T) {
			body := protocolRouteContractRequestBody(route.InboundProtocol())
			originalBody := append([]byte(nil), body...)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, protocolRouteContractInboundPath(route.InboundProtocol()), bytes.NewReader(body))
			upstream := &protocolTargetHTTPUpstream{responses: []*http.Response{protocolRouteContractResponse(route)}}
			svc := protocolTargetTestService(upstream)
			account := protocolTargetTestAccount(route.TargetProtocol())
			wireModel := "wire-model"
			if route.TargetProtocol() == protocolrouter.ProtocolMessages {
				wireModel = "claude-sonnet-4-6"
			}
			account.Credentials["model_mapping"] = map[string]any{"client-model": wireModel}
			account.Credentials["api_base_urls"] = map[string]any{
				APIProtocolAnthropic:       "http://upstream.example",
				APIProtocolChatCompletions: "http://upstream.example",
				APIProtocolResponses:       "http://upstream.example",
			}

			value, err := protocolTargetTestExecution(t, route.InboundProtocol(), body, account, func(
				executionCtx context.Context,
				plan protocolrouter.Plan,
				request protocolrouter.CanonicalRequest,
			) (any, error) {
				if got := request.Body(); !bytes.Equal(got, originalBody) {
					t.Fatalf("canonical request changed before adapter execution: %s", got)
				}
				switch route.AdapterID() {
				case protocolrouter.AdapterMessagesIdentity, protocolrouter.AdapterMessagesToResponses:
					return svc.ForwardAsAnthropic(executionCtx, c, account, request.Body(), "", "")
				case protocolrouter.AdapterMessagesToChat:
					return svc.ForwardAsAnthropicDispatched(executionCtx, c, account, request.Body(), "", "")
				case protocolrouter.AdapterChatIdentity:
					return svc.ForwardAsChatCompletionsDispatched(executionCtx, c, account, request.Body(), "", "")
				case protocolrouter.AdapterChatToResponses, protocolrouter.AdapterChatToMessages:
					return svc.ForwardAsChatCompletions(executionCtx, c, account, request.Body(), "", "")
				case protocolrouter.AdapterResponsesIdentity:
					return svc.ForwardAsResponsesDispatched(executionCtx, c, account, request.Body())
				case protocolrouter.AdapterResponsesToChat, protocolrouter.AdapterResponsesToMessages:
					return svc.Forward(executionCtx, c, account, request.Body())
				default:
					t.Fatalf("unhandled registry adapter %q", plan.AdapterID())
					return nil, nil
				}
			})
			if err != nil {
				t.Fatalf("ExecuteSelectedProtocol: %v", err)
			}
			result, ok := value.(*OpenAIForwardResult)
			if !ok || result == nil {
				t.Fatalf("result type = %T, want *OpenAIForwardResult", value)
			}
			if len(upstream.requests) != 1 {
				t.Fatalf("upstream requests = %d, want 1", len(upstream.requests))
			}
			request := upstream.requests[0]
			if got, want := request.URL.String(), protocolRouteContractEndpoint(route.TargetProtocol()); got != want {
				t.Fatalf("upstream endpoint = %q, want %q", got, want)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer sk-protocol-target" {
				t.Fatalf("authorization = %q, want OpenAI account bearer credential", got)
			}
			if got := request.Header.Get("x-api-key"); got != "" {
				t.Fatalf("x-api-key = %q, want no Anthropic credential header for OpenAI account", got)
			}
			wireBody := readProtocolTargetRequestBody(t, request)
			if got := gjson.GetBytes(wireBody, "model").String(); got != wireModel {
				t.Fatalf("wire model = %q, want %q; body=%s", got, wireModel, wireBody)
			}
			assertProtocolRouteContractBody(t, route.TargetProtocol(), wireBody)
			if !bytes.Equal(body, originalBody) {
				t.Fatalf("caller body mutated: %s", body)
			}
			facts, ok := result.ProtocolRouteFacts()
			if !ok || facts.TargetProtocol() != route.TargetProtocol() || facts.Endpoint() != request.URL.String() {
				t.Fatalf("route facts = %#v, want target %q endpoint %q", facts, route.TargetProtocol(), request.URL.String())
			}
		})
	}
}

func protocolRouteContractRequestBody(protocol protocolrouter.Protocol) []byte {
	switch protocol {
	case protocolrouter.ProtocolMessages:
		return []byte(`{"model":"client-model","max_tokens":8,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	case protocolrouter.ProtocolChatCompletions:
		return []byte(`{"model":"client-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	case protocolrouter.ProtocolResponses:
		return []byte(`{"model":"client-model","input":"hello","stream":false}`)
	default:
		return nil
	}
}

func protocolRouteContractInboundPath(protocol protocolrouter.Protocol) string {
	switch protocol {
	case protocolrouter.ProtocolMessages:
		return "/v1/messages"
	case protocolrouter.ProtocolChatCompletions:
		return "/v1/chat/completions"
	case protocolrouter.ProtocolResponses:
		return "/v1/responses"
	default:
		return "/"
	}
}

func protocolRouteContractEndpoint(protocol protocolrouter.Protocol) string {
	return "http://upstream.example" + protocolRouteContractInboundPath(protocol)
}

func protocolTargetAnthropicBufferedResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`,
		)),
	}
}

func protocolRouteContractResponse(route protocolrouter.RouteSpec) *http.Response {
	body := `{"id":"resp_1","object":"response","status":"completed","model":"wire-model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`
	contentType := "application/json"
	switch route.TargetProtocol() {
	case protocolrouter.ProtocolMessages:
		if route.InboundProtocol() == protocolrouter.ProtocolMessages {
			body = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`
		} else {
			contentType = "text/event-stream"
			body = miniAnthropicSSEStream()
		}
	case protocolrouter.ProtocolChatCompletions:
		body = `{"id":"chatcmpl_1","object":"chat.completion","model":"wire-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
	case protocolrouter.ProtocolResponses:
		contentType = "text/event-stream"
		body = strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"wire-model","output":[]}}`,
			"",
			`data: {"type":"response.output_text.delta","delta":"ok"}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"wire-model","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func assertProtocolRouteContractBody(t *testing.T, protocol protocolrouter.Protocol, body []byte) {
	t.Helper()
	switch protocol {
	case protocolrouter.ProtocolMessages:
		if !gjson.GetBytes(body, "messages").IsArray() || !gjson.GetBytes(body, "max_tokens").Exists() {
			t.Fatalf("messages wire body is invalid: %s", body)
		}
	case protocolrouter.ProtocolChatCompletions:
		if !gjson.GetBytes(body, "messages").IsArray() {
			t.Fatalf("chat completions wire body is invalid: %s", body)
		}
	case protocolrouter.ProtocolResponses:
		if !gjson.GetBytes(body, "input").Exists() {
			t.Fatalf("responses wire body is invalid: %s", body)
		}
	}
}

func readProtocolTargetRequestBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	if request == nil || request.Body == nil {
		t.Fatal("upstream request body is missing")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read upstream request body: %v", err)
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	return body
}
