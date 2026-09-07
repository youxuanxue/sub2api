package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type plannedOpenAIShapeAccountRepo struct {
	service.AccountRepository
	account *service.Account
}

func (r *plannedOpenAIShapeAccountRepo) GetByID(context.Context, int64) (*service.Account, error) {
	return r.account, nil
}

type plannedOpenAIShapeUpstream struct {
	service.HTTPUpstream
	request *http.Request
	body    []byte
}

func (u *plannedOpenAIShapeUpstream) Do(request *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.request = request
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	u.body = body
	code := http.StatusOK
	response := `{"id":"chatcmpl-edge","object":"chat.completion","model":"gemini-3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	contentType := "application/json"
	switch {
	case strings.Contains(request.URL.Path, ":generateContent"):
		response = `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`
	case strings.HasSuffix(request.URL.Path, "/responses"):
		response = `{"id":"resp-edge","object":"response","status":"completed","model":"gemini-3.8-flash","output":[{"type":"message","id":"msg-edge","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
		if gjson.GetBytes(body, "stream").Bool() {
			contentType = "text/event-stream"
			response = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
		}
	case gjson.GetBytes(body, "stream").Bool():
		contentType = "text/event-stream"
		response = "data: " + `{"id":"chatcmpl-edge","object":"chat.completion.chunk","model":"gemini-3.8-flash","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}` + "\n\ndata: [DONE]\n\n"
	}
	if !strings.Contains(request.URL.Path, ":generateContent") && gjson.GetBytes(body, "model").String() == "" {
		code = http.StatusBadRequest
		response = `{"error":{"message":"model is required","type":"invalid_request_error"}}`
	}
	return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response))}, nil
}

func TestGatewayPlannedAntigravityOpenAIShapeKeepsPlannedWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const model = "gemini-3.8-flash"
	const upstreamModel = "gemini-3.8-flash-medium"
	for _, tc := range []struct {
		name    string
		inbound protocolrouter.Protocol
		target  protocolrouter.Protocol
		stream  bool
	}{
		{"chat_identity", protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolChatCompletions, false},
		{"chat_identity_stream", protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolChatCompletions, true},
		{"chat_to_responses", protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses, false},
		{"responses_identity", protocolrouter.ProtocolResponses, protocolrouter.ProtocolResponses, false},
		{"responses_to_chat", protocolrouter.ProtocolResponses, protocolrouter.ProtocolChatCompletions, false},
		{"responses_to_chat_stream", protocolrouter.ProtocolResponses, protocolrouter.ProtocolChatCompletions, true},
		{"chat_to_gemini", protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolGeminiGenerateContent, false},
		{"responses_to_gemini", protocolrouter.ProtocolResponses, protocolrouter.ProtocolGeminiGenerateContent, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &service.Account{
				ID: 62, Name: "antigravity-us4", Platform: service.PlatformAntigravity,
				Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
				Credentials: map[string]any{
					"base_url": "https://api-us4.tokenkey.dev", "api_key": "edge-test-key",
					"model_mapping": map[string]any{model: upstreamModel},
				},
			}
			protocols := []protocolrouter.Protocol{tc.target}
			if tc.target != protocolrouter.ProtocolGeminiGenerateContent {
				protocols = append(protocols, protocolrouter.ProtocolGeminiGenerateContent)
			}
			attachHandlerTestProtocolCapability(t, account, protocols...)
			body := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
			path := "/v1/chat/completions"
			if tc.inbound == protocolrouter.ProtocolResponses {
				path = "/v1/responses"
				body = []byte(`{"model":"gemini-3.8-flash","input":"hello","max_output_tokens":32}`)
			}
			body, err := sjson.SetBytes(body, "stream", tc.stream)
			require.NoError(t, err)
			request, err := newCanonicalProtocolRequest(tc.inbound, protocolrouter.ResponsesPathNone, model, tc.stream, body)
			require.NoError(t, err)
			snapshot, err := service.ProtocolAccountSnapshot(account, model)
			require.NoError(t, err)
			router := service.NewProtocolRouter()
			plan, err := router.Plan(request, snapshot)
			require.NoError(t, err)
			require.Equal(t, tc.target, plan.TargetProtocol())
			require.Equal(t, upstreamModel, plan.ResolvedModel())

			repo := &plannedOpenAIShapeAccountRepo{account: account}
			upstream := &plannedOpenAIShapeUpstream{}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			h := &GatewayHandler{
				protocolRouter:       router,
				gatewayService:       service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil),
				openAIGatewayService: service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil),
				geminiCompatService:  service.NewGeminiMessagesCompatService(repo, nil, nil, nil, nil, nil, upstream, nil, cfg),
			}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
			ctx := service.WithProtocolRouting(c.Request.Context(), router, request)
			c.Request = c.Request.WithContext(ctx)
			selection := &service.AccountSelectionResult{Account: account, ProtocolPlan: &plan}
			var result *service.ForwardResult
			if tc.inbound == protocolrouter.ProtocolChatCompletions {
				result, err = h.executeChatCompletionsSelectedProtocol(c, ctx, selection, account, service.ChannelMappingResult{}, model, nil)
			} else {
				result, err = h.executeResponsesSelectedProtocol(c, ctx, selection, account, service.ChannelMappingResult{}, model, nil)
			}
			require.NoError(t, err, recorder.Body.String())
			require.Equal(t, http.StatusOK, recorder.Code)
			require.NotNil(t, upstream.request)
			require.Equal(t, plan.Endpoint(), upstream.request.URL.String())
			if tc.target == protocolrouter.ProtocolGeminiGenerateContent {
				require.Equal(t, "edge-test-key", upstream.request.Header.Get("x-goog-api-key"))
				require.True(t, gjson.GetBytes(upstream.body, "contents").IsArray())
			} else {
				require.Equal(t, "Bearer edge-test-key", upstream.request.Header.Get("Authorization"))
				require.Empty(t, upstream.request.Header.Get("x-goog-api-key"))
				require.Equal(t, plan.ResolvedModel(), gjson.GetBytes(upstream.body, "model").String())
				require.False(t, gjson.GetBytes(upstream.body, "contents").Exists())
				if tc.target == protocolrouter.ProtocolChatCompletions {
					require.True(t, gjson.GetBytes(upstream.body, "messages").IsArray())
				} else {
					require.True(t, gjson.GetBytes(upstream.body, "input").Exists())
				}
			}
			require.Contains(t, recorder.Body.String(), "OK")
			require.Equal(t, 3, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
		})
	}
}
