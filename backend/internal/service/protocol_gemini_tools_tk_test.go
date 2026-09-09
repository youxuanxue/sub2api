//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProtocolGeminiChatToolsPlansExecutesAndReturnsToolUsage(t *testing.T) {
	tools := make([]any, 11)
	for i := range tools {
		tools[i] = map[string]any{"type": "function", "function": map[string]any{"name": fmt.Sprintf("lookup_%d", i), "parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}}}
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"model": "gemini-3.8-flash", "stream": stream, "stream_options": map[string]any{"include_usage": true}, "max_tokens": 1024, "tool_choice": "auto", "tools": tools,
				"messages": []any{map[string]any{"role": "user", "content": "Look up Paris"}, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_old", "type": "function", "function": map[string]any{"name": "lookup_0", "arguments": `{"city":"Paris"}`}}}}, map[string]any{"role": "tool", "tool_call_id": "call_old", "content": "sunny"}}})
			require.NoError(t, err)
			const response = `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup_1","args":{"city":"London"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3}}`
			upstreamBody, contentType := response, "application/json"
			if stream {
				upstreamBody, contentType = "data: "+response+"\n\ndata: [DONE]\n\n", "text/event-stream"
			}
			httpStub := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(upstreamBody))}}
			svc := &GeminiMessagesCompatService{httpUpstream: httpStub, cfg: &config.Config{}}
			_, _, accounts, _ := candidateGoogleFixture(t)
			account := &accounts[1]
			router := NewProtocolRouter()
			request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gemini-3.8-flash", stream, body)
			require.NoError(t, err)
			ctx := WithProtocolRouting(context.Background(), router, request)
			plan, governed, err := protocolPlanForAccount(ctx, account, "gemini-3.8-flash")
			require.NoError(t, err)
			require.True(t, governed)
			require.Equal(t, protocolrouter.AdapterChatToGemini, plan.AdapterID())
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
			value, err := ExecuteSelectedProtocol(ctx, router, &AccountSelectionResult{Account: account, ProtocolPlan: &plan}, account,
				func(context.Context, *Account, string) error { return nil }, protocolExecutionAccountLoaderForTest(account),
				ProtocolExecutors{ChatToGemini: func(ctx context.Context, a *Account, _ protocolrouter.Plan, req protocolrouter.CanonicalRequest) (any, error) {
					return svc.ForwardAsChatCompletions(ctx, c, a, req.Body())
				}})
			require.NoError(t, err)
			result := value.(*ForwardResult)
			require.Equal(t, 12, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.OutputTokens)
			require.Equal(t, 200, rec.Code)
			require.Contains(t, rec.Body.String(), `"finish_reason":"tool_calls"`)
			require.Contains(t, rec.Body.String(), `"name":"lookup_1"`)
			if stream {
				require.Contains(t, rec.Body.String(), "data: [DONE]")
				require.Contains(t, rec.Body.String(), `"prompt_tokens":12`)
			}
			require.NotNil(t, httpStub.lastReq)
			posted, err := io.ReadAll(httpStub.lastReq.Body)
			require.NoError(t, err)
			require.Len(t, gjson.GetBytes(posted, "tools.0.functionDeclarations").Array(), 11)
			require.Contains(t, string(posted), "functionResponse")
			require.Contains(t, string(posted), "sunny")
			require.Contains(t, string(posted), "Paris")
		})
	}
}

func TestProtocolGeminiMessagesToolsPlansAndConvertsVertex(t *testing.T) {
	resolver, _, accounts, _ := candidateGoogleFixture(t)
	body := []byte(`{"model":"gemini-3.8-flash","max_tokens":1024,"messages":[{"role":"user","content":"Reply with one short sentence."}],"tools":[{"name":"tk_smoke_schema_probe","description":"Do not call.","input_schema":{"type":"object","required":["mode"],"properties":{"mode":{"type":"string","const":"auto"},"limit":{"type":"integer","minimum":1,"exclusiveMinimum":0,"exclusiveMaximum":100}}}}]}`)
	ctx := resolver.WithRequest(context.Background(), ShapeAnthropicMessages, "/v1/messages", "gemini-3.8-flash", body)
	plan, governed, err := protocolPlanForAccount(ctx, &accounts[0], "gemini-3.8-flash")
	require.True(t, governed)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.AdapterMessagesToGemini, plan.AdapterID())
	require.Equal(t, "gemini-3.8-flash", plan.ResolvedModel())
	converted, err := convertClaudeMessagesToGeminiGenerateContent(body)
	require.NoError(t, err)
	require.Equal(t, "tk_smoke_schema_probe", gjson.GetBytes(converted, "tools.0.functionDeclarations.0.name").String())
	require.Equal(t, "mode", gjson.GetBytes(converted, "tools.0.functionDeclarations.0.parameters.required.0").String())
	require.False(t, gjson.GetBytes(converted, "tools.0.functionDeclarations.0.parameters.properties.mode.const").Exists())
	require.Equal(t, int64(1), gjson.GetBytes(converted, "tools.0.functionDeclarations.0.parameters.properties.limit.minimum").Int())
	group := grp(16, PlatformNewAPI, 0, false)
	group.AllowMessagesDispatch = true
	require.True(t, candidatePathAllowsEndpoint(ctx, &accounts[0], &group, ShapeAnthropicMessages, "gemini-3.8-flash", &plan))
	group.AllowMessagesDispatch = false
	require.False(t, candidatePathAllowsEndpoint(ctx, &accounts[0], &group, ShapeAnthropicMessages, "gemini-3.8-flash", &plan))
}
