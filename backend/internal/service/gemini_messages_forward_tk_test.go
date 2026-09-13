//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func geminiMessagesForwardFixture(t *testing.T, stream bool) (*gin.Context, *httptest.ResponseRecorder, *Account, protocolrouter.CanonicalRequest, context.Context) {
	t.Helper()
	account := newAnthropicAPIKeyAccountForTest()
	account.Credentials["base_url"] = "https://messages.example"
	account.Credentials["model_mapping"] = map[string]any{"client-model": "claude-sonnet-4-6"}
	attachTestProtocolCapability(account, protocolrouter.ProtocolMessages)
	body := []byte(`{"contents":[{"parts":[{"text":"hello"}]}]}`)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolGeminiGenerateContent, protocolrouter.ResponsesPathNone, "client-model", stream, body)
	require.NoError(t, err)
	ctx := WithProtocolRouting(context.Background(), NewProtocolRouter(), request)
	plan, governed, err := protocolPlanForAccount(ctx, account, "client-model")
	require.NoError(t, err)
	require.True(t, governed)
	require.Equal(t, protocolrouter.AdapterGeminiToMessages, plan.AdapterID())
	ctx = withProtocolExecutionPlan(ctx, plan)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1beta/models/client-model:generateContent", strings.NewReader(string(body))).WithContext(ctx)
	return c, rec, account, request, ctx
}

const geminiMessagesSSE = "data: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":3,\"cache_read_input_tokens\":2}}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n"

func TestGeminiMessagesForwardTransportUsageAndFailure(t *testing.T) {
	for _, scenario := range []string{"json", "stream", "truncated", "retry", "reject"} {
		t.Run(scenario, func(t *testing.T) {
			stream := scenario != "json"
			c, rec, account, request, ctx := geminiMessagesForwardFixture(t, stream)
			status, ctype := 200, "application/json"
			payload := `{"type":"message","role":"assistant","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"cache_read_input_tokens":2,"output_tokens":7}}`
			if stream {
				ctype = "text/event-stream"
				payload = geminiMessagesSSE
				if scenario != "truncated" {
					payload += "data: {\"type\":\"message_stop\"}\n\n"
				}
			}
			if scenario == "retry" || scenario == "reject" {
				ctype = "application/json"
				status = 503
				if scenario == "reject" {
					status = 400
				}
				payload = `{"error":{"type":"invalid_request_error","message":"private-provider-data"}}`
			}
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{ctype}}, Body: io.NopCloser(strings.NewReader(payload))}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			original := c.Request
			result, err := svc.ForwardGeminiViaMessages(ctx, c, account, request, nil, nil)
			require.Same(t, original, c.Request)
			require.Equal(t, "/v1/messages", upstream.lastReq.URL.Path)
			require.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "messages.0.content.0.text").String())
			require.NotContains(t, rec.Body.String(), "private-provider-data")
			if scenario == "json" || scenario == "stream" {
				require.NoError(t, err)
				require.Contains(t, rec.Body.String(), `"finishReason":"STOP"`)
				require.Equal(t, "client-model", result.Model)
				require.Equal(t, 3, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.CacheReadInputTokens)
				require.Equal(t, 7, result.Usage.OutputTokens)
			} else {
				require.Error(t, err)
				var retry *UpstreamFailoverError
				if scenario == "retry" {
					require.True(t, errors.As(err, &retry))
					require.False(t, c.Writer.Written())
				} else {
					require.False(t, errors.As(err, &retry))
					require.NotContains(t, rec.Body.String(), `"STOP"`)
				}
				if scenario == "truncated" {
					require.NotNil(t, result)
					require.Equal(t, 7, result.Usage.OutputTokens)
					require.Contains(t, rec.Body.String(), `"text":"hello"`)
				}
			}
		})
	}
}
