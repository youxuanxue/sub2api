//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
)

// Regression pin for Wei-Shaw/sub2api#1311 (extended scope).
//
// /v1/chat/completions non-stream requests served by a matched Anthropic
// account take the gateway buffered conversion path
// (handleCCBufferedFromAnthropic): upstream Anthropic returns SSE with
// Content-Type text/event-stream, we buffer the stream and convert via
// Anthropic→Responses→ChatCompletions into a single JSON chat.completion
// object. The response header propagation step copies the upstream
// `Content-Type: text/event-stream` header to the client; gin's c.Data /
// c.JSON do NOT overwrite an already-set Content-Type, so without an explicit
// override the client receives a JSON body whose Content-Type is still
// text/event-stream.

func TestHandleCCBufferedFromAnthropic_NonStream_ReturnsApplicationJSONContentType(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := &http.Response{
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"rid_cc_buffered_content_type"},
		},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_ct_2","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4.5","stop_reason":"","usage":{"input_tokens":4}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
			``,
		}, "\n"))),
	}

	svc := &GatewayService{
		responseHeaderFilter: responseheaders.CompileHeaderFilter(config.ResponseHeaderConfig{}),
	}
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "claude-sonnet-4.5", "claude-sonnet-4.5", nil, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream, "non-stream request must produce non-stream result")

	gotCT := rec.Header().Get("Content-Type")
	require.True(t, strings.HasPrefix(gotCT, "application/json"),
		"non-stream /v1/chat/completions (Anthropic-backed) must emit application/json Content-Type; got %q. See Wei-Shaw/sub2api#1311.", gotCT)
	require.NotContains(t, gotCT, "text/event-stream",
		"Content-Type must not be SSE; got %q. See Wei-Shaw/sub2api#1311.", gotCT)
	require.NotContains(t, rec.Body.String(), "data:", "response body must not be SSE-framed")
}
