//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGeminiChatWriterStreamsBeforeCompletion(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := &geminiChatWriter{ResponseWriter: c.Writer, header: make(http.Header), status: 200, stream: true}
	_, err := w.WriteString(": heartbeat\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n")
	require.NoError(t, err)
	require.False(t, c.Writer.Written())
	_, err = w.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2}}\n\n")
	require.NoError(t, err)
	require.False(t, c.Writer.Written(), "usage alone must not commit a response")
	for _, part := range []string{"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"", "hello\"}}]}\r\n\r\n"} {
		_, err = w.WriteString(part)
		require.NoError(t, err)
	}
	require.Contains(t, rec.Body.String(), `"text":"hello"`)
	require.True(t, rec.Flushed)
	require.NotContains(t, rec.Body.String(), "finishReason")
	require.Error(t, w.finish(), "EOF before terminal must remain an error")
	_, err = w.WriteString("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	require.NoError(t, err)
	require.NoError(t, w.finish())
	require.Contains(t, rec.Body.String(), `"finishReason":"STOP"`)
	require.NotContains(t, rec.Body.String(), "[DONE]")
}

func TestGeminiChatForwardUsesBoundTransportAndUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			payload := `{"choices":[{"index":0,"message":{"content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4,"total_tokens":13,"prompt_tokens_details":{"cached_tokens":3}}}`
			if stream {
				payload = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"total_tokens\":13,\"prompt_tokens_details\":{\"cached_tokens\":3}}}\n\ndata: [DONE]\n\n"
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			c, rec, account, request, ctx := geminiChatForwardFixture(t, stream)
			original := c.Request
			result, err := svc.ForwardGeminiViaChat(ctx, c, account, request)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 6, result.Usage.InputTokens)
			require.Equal(t, 3, result.Usage.CacheReadInputTokens)
			require.Equal(t, 4, result.Usage.OutputTokens)
			require.Equal(t, "gpt-5.4", result.Model)
			require.Equal(t, "wire-model", result.UpstreamModel)
			require.Equal(t, "wire-model", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "messages.0.content").String())
			require.Equal(t, "/v1/chat/completions", upstream.lastReq.URL.Path)
			require.Equal(t, "Bearer sk-test", upstream.lastReq.Header.Get("Authorization"))
			require.Same(t, original, c.Request)
			require.Contains(t, c.Request.URL.Path, "generateContent")
			require.Contains(t, rec.Body.String(), `"promptTokenCount":9`)
			require.Contains(t, rec.Body.String(), `"cachedContentTokenCount":3`)
			require.NotContains(t, rec.Body.String(), `"choices"`)
		})
	}
}

func geminiChatForwardFixture(t *testing.T, stream bool) (*gin.Context, *httptest.ResponseRecorder, *Account, protocolrouter.CanonicalRequest, context.Context) {
	t.Helper()
	account := rawChatCompletionsTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "wire-model"}
	attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions)
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolGeminiGenerateContent, protocolrouter.ResponsesPathNone, "gpt-5.4", stream, body)
	require.NoError(t, err)
	ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
	plan, governed, err := protocolPlanForAccount(ctx, account, "gpt-5.4")
	require.NoError(t, err)
	require.True(t, governed)
	ctx = withProtocolExecutionPlan(ctx, plan)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1beta/models/gpt-5.4:generateContent?alt=sse", strings.NewReader(string(body))).WithContext(ctx)
	return c, rec, account, request, ctx
}

func TestGeminiChatForwardFailureAndPartialUsage(t *testing.T) {
	for _, scenario := range []string{"retry", "terminal_error", "truncated", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			c, rec, account, request, ctx := geminiChatForwardFixture(t, true)
			status, payload := 503, `{"error":{"message":"private-provider-data"}}`
			switch scenario {
			case "terminal_error":
				status = 400
			case "truncated":
				status = 200
				payload = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n"
			case "malformed":
				status = 200
				payload = "data: not-json\n\n"
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			result, err := svc.ForwardGeminiViaChat(ctx, c, account, request)
			require.Error(t, err)
			var failover *UpstreamFailoverError
			if scenario == "retry" {
				require.True(t, errors.As(err, &failover))
				require.False(t, c.Writer.Written())
			} else {
				require.False(t, errors.As(err, &failover))
				require.Contains(t, rec.Body.String(), `"error"`)
			}
			require.NotContains(t, rec.Body.String(), "private-provider-data")
			require.NotContains(t, rec.Body.String(), `"STOP"`)
			if scenario == "truncated" {
				require.NotNil(t, result)
				require.Equal(t, 7, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
				require.Contains(t, rec.Body.String(), `"text":"partial"`)
			}
		})
	}
}

func TestGeminiChatCandidateUsesSharedScopeAndPriority(t *testing.T) {
	for _, direct := range []bool{false, true} {
		_, _, native, _ := candidateGoogleFixture(t)
		native[0].GroupIDs = []int64{10}
		native[0].Priority = 10
		chat := globalCandidateAccount(999, 1, 10)
		chat.Credentials["model_mapping"] = map[string]any{"gemini-3.8-flash": "wire-model"}
		attachTestProtocolCapability(&chat, protocolrouter.ProtocolChatCompletions)
		groups := []Group{grp(10, PlatformOpenAI, 1, false)}
		r, _, key := globalCandidateFixture(groups, []Account{native[0], chat})
		if direct {
			key.RoutingMode = RoutingModeDirect
			key.GroupID = &groups[0].ID
			key.Group = &groups[0]
		}
		body := []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`)
		ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeGemini, "/v1beta/models/gemini-3.8-flash:generateContent", "gemini-3.8-flash", body, "", "")
		require.NoError(t, err)
		require.Equal(t, chat.ID, state.current.account.ID, "native identity cannot override shared account priority")
		selection, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
		require.NoError(t, err)
		require.Equal(t, protocolrouter.AdapterGeminiToChat, selection.ProtocolPlan.AdapterID())
		selection.ReleaseFunc()
		selection, err = state.selectAccount(ctx, candidateSelectOptions{acquire: true, excluded: map[int64]struct{}{chat.ID: {}}})
		require.NoError(t, err)
		require.Equal(t, native[0].ID, selection.Account.ID)
		selection.ReleaseFunc()
	}
}
