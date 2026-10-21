//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestKiroStreaming_DelayedToolResponseMatchesFastResponse(t *testing.T) {
	input := map[string]any{"file_path": "目录/说明.md", "content": "重复重复🙂\n中文"}
	upstreamBody := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"重复"}`))
	upstreamBody = append(upstreamBody, buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"重复🙂"}`))...)
	upstreamBody = append(upstreamBody, kiroToolUseEvent("tool_a", "Write", input)...)
	upstreamBody = append(upstreamBody, kiroToolUseEvent("tool_b", "Read", map[string]any{"file_path": "目录/说明.md"})...)
	upstreamBody = appendKiroTerminalStop(upstreamBody, "TOOL_USE")
	var baseline []map[string]any
	for _, delay := range []time.Duration{0, 2100 * time.Millisecond} {
		c, rec := newKeepaliveTestContext(t)
		svc := NewKiroGatewayService(&kiroStallUpstream{respBody: &delayedKiroBodyReader{delay: delay, body: append([]byte(nil), upstreamBody...)}}, nil, nil)
		svc.SetGatewayConfig(&config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1}})
		body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"更新说明"}],"max_tokens":1024,"stream":true}`)
		result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5-5", Stream: true}, time.Now())
		require.NoError(t, err)
		require.True(t, utf8.Valid(rec.Body.Bytes()))
		events := kiroReplayEvents(t, rec.Body.String())
		require.Equal(t, "message_start", events[0]["type"])
		msg := events[0]["message"].(map[string]any)
		require.Equal(t, result.RequestID, msg["id"])
		require.Equal(t, result.RequestID, rec.Header().Get("x-request-id"))
		require.Equal(t, "claude-opus-5-5", msg["model"])
		require.Equal(t, "message_stop", events[len(events)-1]["type"])
		delta := events[len(events)-2]
		require.Equal(t, "tool_use", delta["delta"].(map[string]any)["stop_reason"])
		require.Equal(t, float64(result.Usage.InputTokens), delta["usage"].(map[string]any)["input_tokens"])
		var contentEvents []map[string]any
		starts, stops := 0, 0
		text := ""
		inputs := map[float64]map[string]any{}
		for _, e := range events {
			switch e["type"] {
			case "message_start":
				starts++
			case "message_stop":
				stops++
			case "content_block_start", "content_block_stop":
				contentEvents = append(contentEvents, e)
			case "content_block_delta":
				contentEvents = append(contentEvents, e)
				d := e["delta"].(map[string]any)
				if d["type"] == "text_delta" {
					text += d["text"].(string)
				}
				if d["type"] == "input_json_delta" {
					var v map[string]any
					require.NoError(t, json.Unmarshal([]byte(d["partial_json"].(string)), &v))
					inputs[e["index"].(float64)] = v
				}
			}
		}
		require.Equal(t, 1, starts)
		require.Equal(t, 1, stops)
		require.Equal(t, "重复重复🙂", text, "valid repeated deltas must not be deduplicated")
		require.Equal(t, input, inputs[1])
		require.Equal(t, map[string]any{"file_path": "目录/说明.md"}, inputs[2])
		if delay == 0 {
			baseline = contentEvents
		} else {
			require.Equal(t, baseline, contentEvents)
			require.GreaterOrEqual(t, strings.Count(rec.Body.String(), "event: ping"), 2)
		}
	}
}

func kiroReplayEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data:") {
			var e map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &e))
			events = append(events, e)
		}
	}
	return events
}

func TestKiroGatewayService_ReasoningOnlyCannotCompleteEmptyTurn(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			c, _ := newKeepaliveTestContext(t)
			frames := buildKiroEventStreamMessage("reasoningContentEvent", []byte(`{"text":"hidden reasoning without an answer"}`))
			frames = appendKiroTerminalStop(frames, "END_TURN")
			svc := NewKiroGatewayService(&kiroFakeUpstream{body: frames}, nil, nil)
			body, _ := json.Marshal(map[string]any{"model": "claude-opus-5", "messages": []map[string]any{{"role": "user", "content": "hi"}}, "max_tokens": 1024, "stream": stream})
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5", Stream: stream}, time.Now())
			require.Error(t, err, "hidden reasoning is not a client-visible answer")
			require.Nil(t, result)
			require.False(t, c.Writer.Written(), "empty response must remain eligible for the existing failure handler")
		})
	}
}

// Inspect the actual outbound payload, not the parsed input DTO.
type kiroReplayRequestCapture struct {
	kiroFakeUpstream
	request []byte
}

func (u *kiroReplayRequestCapture) DoWithTLS(r *http.Request, p string, a int64, n int, fp *tlsfingerprint.Profile) (*http.Response, error) {
	var err error
	u.request, err = io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return u.kiroFakeUpstream.DoWithTLS(r, p, a, n, fp)
}
func TestKiroGatewayService_OutputSchemaReachesUpstream(t *testing.T) {
	c, _ := newKeepaliveTestContext(t)
	up := &kiroReplayRequestCapture{kiroFakeUpstream: kiroFakeUpstream{body: kiroTextStopStream(`{"completed":true}`, "END_TURN")}}
	svc := NewKiroGatewayService(up, nil, nil)
	body := []byte(`{"model":"claude-opus-4-7","max_tokens":1024,"thinking":{"type":"adaptive"},"output_config":{"effort":"high","format":{"type":"json_schema","schema":{"type":"object","properties":{"completed":{"type":"boolean"}},"required":["completed"],"additionalProperties":false}}},"messages":[{"role":"user","content":"Report completion"}]}`)
	original := append([]byte(nil), body...)
	_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-7"}, time.Now())
	require.NoError(t, err)
	content := gjson.GetBytes(up.request, "conversationState.currentMessage.userInputMessage.content").String()
	require.Contains(t, content, "Report completion")
	require.Contains(t, content, `"required":["completed"]`)
	require.Equal(t, "high", gjson.GetBytes(up.request, "additionalModelRequestFields.output_config.effort").String())
	require.False(t, gjson.GetBytes(up.request, "additionalModelRequestFields.output_config.format").Exists(), "reuse the Bedrock schema fallback instead of forwarding an unsupported native field")
	require.Equal(t, original, body, "raw capture input is immutable")
}
