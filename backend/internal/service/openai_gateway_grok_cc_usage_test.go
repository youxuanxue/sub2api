//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardAsRawChatCompletions_GrokReasoningUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, inclusive := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/inclusive=%t", stream, inclusive), func(t *testing.T) {
				completion := 37
				if inclusive {
					completion = 227
				}
				usage := fmt.Sprintf(`{"prompt_tokens":668,"completion_tokens":%d,"total_tokens":895,"completion_tokens_details":{"reasoning_tokens":190},"prompt_tokens_details":{"cached_tokens":512}}`, completion)
				payload := `{"id":"chatcmpl_grok","model":"grok-4.6","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":` + usage + `}`
				contentType := "application/json"
				if stream {
					contentType = "text/event-stream"
					payload = "data: {\"id\":\"chatcmpl_grok\",\"model\":\"grok-4.6\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" +
						`data: {"id":"chatcmpl_grok","model":"grok-4.6","choices":[],"usage":` + usage + "}\n\ndata: [DONE]\n\n"
				}
				body := []byte(fmt.Sprintf(`{"model":"grok-4.6","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream))
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(payload)),
				}}}
				account := &Account{ID: 65, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api-us4.tokenkey.dev"}}
				result, err := svc.forwardAsRawChatCompletions(context.Background(), c, account, body, "")
				require.NoError(t, err)
				require.Equal(t, 227, result.Usage.OutputTokens)
				require.Equal(t, 512, result.Usage.CacheReadInputTokens)
				response := rec.Body.String()
				if stream {
					require.Contains(t, response, "data: [DONE]")
					for _, line := range strings.Split(response, "\n") {
						if data, ok := extractOpenAISSEDataLine(line); ok && gjson.Get(data, "usage").IsObject() {
							response = data
							break
						}
					}
				}
				require.Equal(t, int64(227), gjson.Get(response, "usage.completion_tokens").Int())
				require.Equal(t, int64(895), gjson.Get(response, "usage.total_tokens").Int())
				require.Equal(t, int64(190), gjson.Get(response, "usage.completion_tokens_details.reasoning_tokens").Int())
				require.Equal(t, int64(512), gjson.Get(response, "usage.prompt_tokens_details.cached_tokens").Int())
				require.Equal(t, response, string(applyGrokRawChatCompletionsUsage(account, []byte(response))))
			})
		}
	}
}

func TestApplyGrokRawChatCompletionsUsage_PreservesOtherPayloads(t *testing.T) {
	for _, body := range []string{
		`{"usage":{"prompt_tokens":668,"completion_tokens":37,"total_tokens":895,"completion_tokens_details":{"reasoning_tokens":190}}}`,
		`{"usage":null}`, `{"usage":{"completion_tokens":37}}`, `{"usage":`, `[DONE]`, "",
	} {
		for _, account := range []*Account{nil, {Platform: PlatformOpenAI}} {
			require.Equal(t, body, string(applyGrokRawChatCompletionsUsage(account, []byte(body))))
		}
	}
	account := &Account{Platform: PlatformGrok}
	for _, body := range []string{
		`{"usage":{"prompt_tokens":668,"completion_tokens":37,"total_tokens":1000,"completion_tokens_details":{"reasoning_tokens":190}}}`,
		`{"usage":{"prompt_tokens":668,"completion_tokens":37,"completion_tokens_details":{"reasoning_tokens":190}}}`,
		`{"usage":{"prompt_tokens":668,"completion_tokens":227,"total_tokens":895,"completion_tokens_details":{"reasoning_tokens":190}}}`,
	} {
		require.Equal(t, body, string(applyGrokRawChatCompletionsUsage(account, []byte(body))))
	}
	for _, line := range []string{"event: message", "", "data: [DONE]", "data: {broken", `data: {"usage":null}`} {
		require.Equal(t, line, applyGrokRawChatCompletionsUsageSSELine(account, line))
	}
}
