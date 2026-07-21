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

// TestHandleAnthropicStreamingResponse_ReasoningStructureFramesBeforeDelta verifies
// that /v1/messages HTTP forwarding opens a thinking content block as soon as
// upstream emits reasoning lifecycle frames, without waiting for the first summary delta.
func TestHandleAnthropicStreamingResponse_ReasoningStructureFramesBeforeDelta(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamSSE := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_reasoning","model":"gpt-5.6"}}`,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","status":"in_progress"}}`,
		``,
		`data: {"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0,"item_id":"rs_1","part":{"type":"summary_text"}}`,
		``,
		`data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"First token"}`,
		``,
		`data: {"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0}`,
		``,
		`data: {"type":"response.output_text.delta","output_index":1,"delta":"Answer"}`,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_reasoning","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`,
		``,
	}, "\n")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"req-reasoning-progress"}},
		Body:       io.NopCloser(bytes.NewBufferString(upstreamSSE)),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	svc := &OpenAIGatewayService{}
	result, err := svc.handleAnthropicStreamingResponse(
		resp, c, nil, "gpt-5.6", "gpt-5.6", "gpt-5.6", time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	body := rec.Body.String()
	t.Logf("rendered SSE:\n%s", body)

	assertSSEContainsEvent(t, body, "content_block_start", func(data string) bool {
		return gjson.Get(data, "content_block.type").String() == "thinking" &&
			gjson.Get(data, "index").Int() == 0
	}, "thinking block must open before first delta")

	thinkingStartIdx := strings.Index(body, `"type":"thinking"`)
	thinkingDeltaIdx := strings.Index(body, `"type":"thinking_delta"`)
	require.NotEqual(t, -1, thinkingStartIdx)
	require.NotEqual(t, -1, thinkingDeltaIdx)
	assert.Less(t, thinkingStartIdx, thinkingDeltaIdx,
		"thinking content_block_start must precede first thinking_delta")

	assertSSEContainsEvent(t, body, "content_block_delta", func(data string) bool {
		return gjson.Get(data, "delta.type").String() == "thinking_delta" &&
			gjson.Get(data, "delta.thinking").String() == "First token"
	}, "first reasoning delta must map to thinking_delta")
}
