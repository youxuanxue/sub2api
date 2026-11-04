//go:build unit

package service

import (
	"bytes"
	"fmt"
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

// 2026-11-04 cn-prod kimi-k3 benchmark: requests with explicit thinking/effort
// fields returned usage 0/0. These cases pin that the gateway itself keeps a
// realistic Volc Agent Plan usage object for every such shape, on the direct
// (group key) and universal candidate paths, stream and non-stream.
const volcKimiUsageNonStream = `{"id":"x","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"205","reasoning_content":"r"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":30,"total_tokens":50,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":25}}}`

const volcKimiUsageStream = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"r\"}}]}\n\n" +
	"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"205\"}}]}\n\n" +
	"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":30,\"total_tokens\":50,\"prompt_tokens_details\":{\"cached_tokens\":4},\"completion_tokens_details\":{\"reasoning_tokens\":25}}}\n\n" +
	"data: [DONE]\n\n"

var volcKimiThinkingUsageShapes = []string{
	``,
	`,"thinking":{"type":"enabled"}`,
	`,"thinking":{"type":"disabled"}`,
	`,"enable_thinking":false`,
	`,"chat_template_kwargs":{"thinking":false}`,
	`,"reasoning_effort":"none"`,
	`,"reasoning_effort":"low"`,
	`,"reasoning_effort":"high"`,
	`,"reasoning_effort":"max"`,
}

func TestVolcKimiThinkingShapesKeepUpstreamUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"direct", "candidate"} {
		for _, stream := range []bool{false, true} {
			for _, shape := range volcKimiThinkingUsageShapes {
				t.Run(fmt.Sprintf("%s/stream=%v/%s", route, stream, shape), func(t *testing.T) {
					account := volcAgentPlanKimiAccountForTest()
					body := []byte(fmt.Sprintf(`{"model":"kimi-k3","stream":%v,"messages":[{"role":"user","content":"hi"}]%s}`, stream, shape))
					ctx := t.Context()
					if route == "candidate" {
						account.GroupIDs, account.Priority = []int64{10}, 1
						attachTestProtocolCapability(account, protocolrouter.ProtocolChatCompletions)
						r, _, key := globalCandidateFixture([]Group{grp(10, PlatformNewAPI, 1, false)}, []Account{*account})
						var request *CandidateRequest
						var err error
						ctx, request, err = r.PrepareCandidateRequest(ctx, key, ShapeOpenAIChat, "/v1/chat/completions", "kimi-k3", body, "", "")
						require.NoError(t, err)
						require.NotNil(t, request.current.plan)
						ctx = withProtocolExecutionPlan(ctx, *request.current.plan)
						require.True(t, candidateChatReplayable(request, account, ctx, body))
					}
					payload, contentType := volcKimiUsageNonStream, "application/json"
					if stream {
						payload, contentType = volcKimiUsageStream, "text/event-stream"
					}
					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {contentType}},
						Body:       io.NopCloser(strings.NewReader(payload)),
					}}
					svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)

					result, err := svc.ForwardAsChatCompletionsDispatched(ctx, c, account, body, "", "")
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, 20, result.Usage.InputTokens)
					require.Equal(t, 30, result.Usage.OutputTokens)
					require.Equal(t, 4, result.Usage.CacheReadInputTokens)
					require.True(t, strings.HasSuffix(upstream.lastReq.URL.String(), "/api/plan/v3/chat/completions"))
					if stream {
						require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
						require.Contains(t, rec.Body.String(), `"prompt_tokens":20`)
					} else {
						require.EqualValues(t, 20, gjson.Get(rec.Body.String(), "usage.prompt_tokens").Int())
					}
				})
			}
		}
	}
}
