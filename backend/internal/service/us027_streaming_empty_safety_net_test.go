//go:build unit

package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestUS027_StreamingEmptyResponse_SynthesizesEmptyTextBlock simulates the
// production bug: the upstream Codex Responses backend returns a stream that
// only contains response.created and response.completed (with empty output),
// no delta events at all. Without the safety net, the gateway would forward
// message_start → message_delta → message_stop with zero content blocks,
// which Claude Code persists into its session JSONL and then crashes on
// reload (anthropics/claude-code#24662).
//
// AC-001 (positive): the client must receive at least one content_block_start
// + content_block_stop pair, with the start carrying type=text and text="".
// AC-002 (regression guard): the message_delta + message_stop must still arrive
// after the synthesized blocks (Claude Code requires the close envelope).
func TestUS027_StreamingEmptyResponse_SynthesizesEmptyTextBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_empty","model":"gpt-5.2"}}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_empty","status":"completed","output":[],"usage":{"input_tokens":42,"output_tokens":0}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"req-us027"}},
		Body:       io.NopCloser(bytes.NewBufferString(upstreamSSE)),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &OpenAIGatewayService{} // cfg=nil, responseHeaderFilter=nil — both fine for this path

	result, err := svc.handleAnthropicStreamingResponse(
		resp, c,
		"claude-opus-4-6", // originalModel
		"gpt-5.2",         // billingModel
		"gpt-5.2",         // upstreamModel
		time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 42, result.Usage.InputTokens, "input tokens must propagate from terminal event")
	assert.Equal(t, 0, result.Usage.OutputTokens, "output tokens=0 reflects the upstream's actual response")

	body := rec.Body.String()
	t.Logf("rendered SSE:\n%s", body)

	// AC-001: synthesized content block must be present.
	assertSSEContainsEvent(t, body, "content_block_start", func(data string) bool {
		return gjson.Get(data, "content_block.type").String() == "text" &&
			gjson.Get(data, "content_block.text").String() == "" &&
			gjson.Get(data, "index").Int() == 0
	}, "expected synthesized content_block_start with empty text at index=0")

	assertSSEContainsEvent(t, body, "content_block_stop", func(data string) bool {
		return gjson.Get(data, "index").Int() == 0
	}, "expected synthesized content_block_stop at index=0")

	// AC-002: message envelope must still close cleanly.
	assertSSEContainsEvent(t, body, "message_start", func(data string) bool {
		return gjson.Get(data, "message.id").String() == "resp_empty"
	}, "expected message_start with upstream response id")
	assertSSEContainsEvent(t, body, "message_delta", func(data string) bool {
		return gjson.Get(data, "delta.stop_reason").String() == "end_turn" &&
			gjson.Get(data, "usage.input_tokens").Int() == 42
	}, "expected message_delta with end_turn + input usage")
	assertSSEContainsEvent(t, body, "message_stop", func(data string) bool { return true },
		"expected message_stop")

	// Negative ordering check: synthesized content_block_start must precede message_delta.
	startIdx := strings.Index(body, "event: content_block_start")
	deltaIdx := strings.Index(body, "event: message_delta")
	require.NotEqual(t, -1, startIdx)
	require.NotEqual(t, -1, deltaIdx)
	assert.Less(t, startIdx, deltaIdx,
		"content_block_start must appear before message_delta — Anthropic clients require blocks to close before message close")
}

// TestUS027_StreamingNormalText_DoesNotDoubleEmit guards against regression in
// the happy path: when the upstream sends real text deltas, the safety net
// must NOT inject extra synthesized blocks (no double output, no index drift).
func TestUS027_StreamingNormalText_DoesNotDoubleEmit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_ok","model":"gpt-5.2"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		``,
		`data: {"type":"response.output_text.delta","delta":" world"}`,
		``,
		`data: {"type":"response.output_text.done"}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_ok","status":"completed","usage":{"input_tokens":10,"output_tokens":2}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"req-us027-ok"}},
		Body:       io.NopCloser(bytes.NewBufferString(upstreamSSE)),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &OpenAIGatewayService{}
	_, err := svc.handleAnthropicStreamingResponse(
		resp, c, "claude-opus-4-6", "gpt-5.2", "gpt-5.2", time.Now(),
	)
	require.NoError(t, err)

	body := rec.Body.String()
	t.Logf("rendered SSE:\n%s", body)

	// Exactly one content_block_start (the real one, type=text).
	assert.Equal(t, 1, strings.Count(body, "event: content_block_start"),
		"normal streaming must not trigger synthesized blocks")
	assert.Equal(t, 1, strings.Count(body, "event: content_block_stop"))
	// Real text must be in delta (not in start).
	assertSSEContainsEvent(t, body, "content_block_delta", func(data string) bool {
		return gjson.Get(data, "delta.type").String() == "text_delta" &&
			gjson.Get(data, "delta.text").String() == "Hello"
	}, "expected first real text_delta='Hello'")
	assertSSEContainsEvent(t, body, "content_block_delta", func(data string) bool {
		return gjson.Get(data, "delta.text").String() == " world"
	}, "expected second real text_delta=' world'")
}

// assertSSEContainsEvent scans an SSE body for an `event: <eventType>` line
// followed by a `data: <payload>` line whose JSON satisfies the predicate.
// Multiple events of the same type may exist; the first match wins.
func assertSSEContainsEvent(t *testing.T, body, eventType string, match func(data string) bool, msg string) {
	t.Helper()
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines)-1; i++ {
		if lines[i] != "event: "+eventType {
			continue
		}
		dataLine := strings.TrimPrefix(lines[i+1], "data: ")
		if dataLine == lines[i+1] {
			continue
		}
		if match(dataLine) {
			return
		}
	}
	assert.Fail(t, msg, "event=%q not found or predicate failed; body=%q", eventType, body)
}
