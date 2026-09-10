//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	kiro "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroGatewayService_EndTurnDoesNotContinue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		for _, claudeCode := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/claude_code=%t", stream, claudeCode), func(t *testing.T) {
				upstream := &kiroSequenceUpstream{bodies: [][]byte{
					kiroTextStopStream("NO", "END_TURN"),
					kiroToolUseStream("unauthorized-read", "read", map[string]any{"file_path": "/fixture/unconfirmed"}),
				}}
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				parsed := newKiroToolRequestForTest(stream, claudeCode, "Read")
				var request kiro.ClaudeRequest
				require.NoError(t, json.Unmarshal(parsed.Body.Bytes(), &request))
				request.Messages = []kiro.ClaudeMessage{
					{Role: "user", Content: "Read /fixture/unconfirmed"},
					{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "id": "missing", "name": "Read", "input": map[string]any{"file_path": "/fixture/unconfirmed"}}}},
					{Role: "user", Content: "Was that execution confirmed successful? Answer only YES or NO. Do not call tools."},
				}
				raw, err := json.Marshal(request)
				require.NoError(t, err)
				parsed.Body = NewRequestBodyRef(raw)
				result, err := NewKiroGatewayService(upstream, nil, nil).Forward(context.Background(), ctx, newKiroAccountForTest(), parsed, time.Now())
				require.NoError(t, err)
				require.Equal(t, 1, len(upstream.requests))
				require.Equal(t, kiro.EstimateOutputTokens("NO", "", nil), result.Usage.OutputTokens)
				require.Contains(t, recorder.Body.String(), "NO")
				require.Contains(t, recorder.Body.String(), "end_turn")
				require.NotContains(t, recorder.Body.String(), "unauthorized-read")
			})
		}
	}
}

// A progress report is not proof of task completion. Nor does it authorize the
// gateway to substitute another user instruction for the client's current one.
func TestKiroGatewayService_ClaudeCodeTerminalOutcomes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct{ text, stop, want string }{
			{"I will inspect the file next.", "END_TURN", "end_turn"},
			{"The Edit failed. Should I retry?", "END_TURN", "end_turn"},
			{"Partial output", "MAX_TOKENS", "max_tokens"},
			{"Context exhausted", "MODEL_CONTEXT_WINDOW_EXCEEDED", "model_context_window_exceeded"},
			{"I cannot comply.", "CONTENT_FILTERED", "refusal"},
		} {
			t.Run(fmt.Sprintf("%t/%s/%s", stream, tc.stop, tc.text), func(t *testing.T) {
				upstream := &kiroSequenceUpstream{bodies: [][]byte{kiroTextStopStream(tc.text, tc.stop)}}
				rec := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(rec)
				_, err := NewKiroGatewayService(upstream, nil, nil).Forward(context.Background(), ctx, newKiroAccountForTest(), newClaudeCodeKiroParsedRequestForTest(stream), time.Now())
				require.NoError(t, err)
				require.Equal(t, 1, upstream.calls)
				require.Contains(t, rec.Body.String(), tc.text)
				require.Contains(t, rec.Body.String(), fmt.Sprintf(`"stop_reason":%q`, tc.want))
			})
		}
	}
}
