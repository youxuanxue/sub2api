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

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroGatewayService_PreservesIncrementalText(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			var frames []byte
			for _, chunk := range []string{"scre", "en", "\n", "foo", "foo", "\n", "sha256=", "1111", "1111", "2222", "2222"} {
				payload, err := json.Marshal(map[string]string{"content": chunk})
				require.NoError(t, err)
				frames = append(frames, buildKiroEventStreamMessage("assistantResponseEvent", payload)...)
			}
			upstream := &kiroSequenceUpstream{bodies: [][]byte{appendKiroTerminalStop(frames, "END_TURN")}}
			svc := NewKiroGatewayService(upstream, nil, nil)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(streaming), time.Now())
			require.NoError(t, err)
			require.Equal(t, 1, upstream.calls)
			var got strings.Builder
			if streaming {
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event struct {
						Type  string `json:"type"`
						Delta struct {
							Text string `json:"text"`
						} `json:"delta"`
					}
					require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
					if event.Type == "content_block_delta" {
						got.WriteString(event.Delta.Text)
					}
				}
			} else {
				var response struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
				for _, block := range response.Content {
					got.WriteString(block.Text)
				}
			}
			require.Equal(t, "screen\nfoofoo\nsha256=1111111122222222", got.String())
		})
	}
}

func TestKiroGatewayService_InvalidToolNeverCompletesSuccessfully(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, visibleText := range []bool{false, true} {
			for _, event := range []string{
				`{"toolUseId":"bad","name":"Bash","input":"{","stop":true}`,
				`{"toolUseId":"bad","name":"Bash","input":{"command":"echo should-not-run"}}`,
			} {
				t.Run(fmt.Sprintf("stream=%t/text=%t/event=%s", streaming, visibleText, event), func(t *testing.T) {
					var frames []byte
					if visibleText {
						frames = buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"Checking the fixture."}`))
					}
					frames = append(frames, buildKiroEventStreamMessage("toolUseEvent", []byte(event))...)
					upstream := &kiroSequenceUpstream{bodies: [][]byte{appendKiroTerminalStop(frames, "TOOL_USE")}}
					svc := NewKiroGatewayService(upstream, nil, nil)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroToolRequestForTest(streaming, false, "Bash"), time.Now())
					require.Error(t, err)
					require.Nil(t, result)
					body := rec.Body.String()
					require.NotContains(t, body, `"type":"tool_use"`)
					require.NotContains(t, body, "should-not-run")
					require.NotContains(t, body, "event: message_delta")
					require.NotContains(t, body, "event: message_stop")
					require.NotContains(t, body, `"stop_reason":"end_turn"`)
					if streaming && visibleText {
						require.Contains(t, body, "Checking the fixture.")
						require.Contains(t, body, "event: error")
						require.Equal(t, 1, upstream.calls, "visible output must not be replayed")
					}
				})
			}
		}
	}
}

func TestKiroGatewayService_InvalidToolDoesNotReplayEarlierTool(t *testing.T) {
	frames := kiroToolUseEvent("valid", "Bash", map[string]any{"command": "echo once"})
	frames = append(frames, buildKiroEventStreamMessage("toolUseEvent", []byte(`{"toolUseId":"bad","name":"Bash","input":"{","stop":true}`))...)
	upstream := &kiroSequenceUpstream{bodies: [][]byte{appendKiroTerminalStop(frames, "TOOL_USE")}}
	svc := NewKiroGatewayService(upstream, nil, nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroToolRequestForTest(true, false, "Bash"), time.Now())
	require.Error(t, err)
	require.Equal(t, 1, upstream.calls)
	require.Equal(t, 1, strings.Count(rec.Body.String(), `"id":"valid"`))
	require.NotContains(t, rec.Body.String(), `"id":"bad"`)
	require.Contains(t, rec.Body.String(), "event: error")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
}
