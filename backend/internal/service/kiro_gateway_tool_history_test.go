//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroGatewayService_ContinuationPreservesToolHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			parsed := newClaudeCodeKiroParsedRequestForTest(stream)
			var body map[string]any
			require.NoError(t, json.Unmarshal(parsed.Body.Bytes(), &body))
			body["tools"] = []any{map[string]any{"name": "Read", "description": "Read fixture", "input_schema": map[string]any{"type": "object"}}}
			messages := []any{map[string]any{"role": "user", "content": "Read fixtures"}}
			for i := 0; i < 2; i++ {
				id := fmt.Sprintf("fixture-%d", i)
				messages = append(messages,
					map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Read", "input": map[string]any{"path": id}}}},
					map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": i == 0, "content": "result " + id}}},
				)
			}
			body["messages"] = messages
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			parsed.Body = NewRequestBodyRef(raw)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroTextStopStream("I received the results.", "END_TURN"),
				kiroCompletionSignalStream("blocked", "Which fixture should I retry?"),
			}}
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			_, err = NewKiroGatewayService(upstream, nil, nil).Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
			require.NoError(t, err)
			require.Len(t, upstream.requests, 2)
			for _, request := range upstream.requests {
				var payload kiroproto.KiroPayload
				require.NoError(t, json.Unmarshal(request, &payload))
				var calls []kiroproto.KiroToolUse
				var results []kiroproto.KiroToolResult
				for _, message := range payload.ConversationState.History {
					if a := message.AssistantResponseMessage; a != nil {
						calls = append(calls, a.ToolUses...)
					}
					if u := message.UserInputMessage; u != nil {
						require.NotContains(t, u.Content, "result fixture-")
						if u.UserInputMessageContext != nil {
							results = append(results, u.UserInputMessageContext.ToolResults...)
						}
					}
				}
				current := payload.ConversationState.CurrentMessage.UserInputMessage
				require.NotContains(t, current.Content, "result fixture-")
				results = append(results, current.UserInputMessageContext.ToolResults...)
				require.Len(t, calls, 2)
				require.Len(t, results, 2)
				require.Equal(t, "fixture-0", calls[0].ToolUseID)
				require.Equal(t, "fixture-0", results[0].ToolUseID)
				require.Equal(t, "error", results[0].Status)
				require.Equal(t, 1, strings.Count(results[0].Content[0].Text, "[Tool execution failed]"))
				require.Equal(t, "result fixture-1", results[1].Content[0].Text)
			}
		})
	}
}
