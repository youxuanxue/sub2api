package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// These tests pin the upstream Wei-Shaw/sub2api#2245 short-stream buffer on the
// OpenAI Responses passthrough path: when gateway.responses_short_stream_buffer_bytes
// is set, body content is held in a byte window before the first flush so an
// upstream that EOFs before a terminal event fails over cleanly instead of
// shipping a half-finished HTTP 200 SSE.

func newShortStreamPassthroughService(bufBytes int) *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize:                     defaultMaxLineSize,
			ResponsesShortStreamBufferBytes: bufBytes,
		},
	}}
}

// Content shorter than the window, then EOF with no terminal event: nothing is
// flushed to the client and the request fails over.
func TestOpenAIStreamingPassthrough_ShortStreamBuffer_FailoverOnEarlyEOF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newShortStreamPassthroughService(4096)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			"event: response.created",
			`data: {"type":"response.created","response":{"id":"resp_1"}}`,
			"",
			`data: {"type":"response.output_item.added","item":{"type":"message"},"output_index":0}`,
			"",
		}, "\n"))),
		Header: http.Header{"X-Request-Id": []string{"rid-short-eof"}},
	}

	_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}, time.Now(), "", "")
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.False(t, c.Writer.Written(), "no content should be flushed to the client")
	require.Empty(t, rec.Body.String(), "client must not receive a half-finished SSE 200")
}

// Content shorter than the window followed by a terminal event: the held
// content is flushed and the request succeeds.
func TestOpenAIStreamingPassthrough_ShortStreamBuffer_FlushesOnTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newShortStreamPassthroughService(4096)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_item.added","item":{"type":"message"},"output_index":0}`,
			"",
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":2,"output_tokens":3}}}`,
			"",
		}, "\n"))),
		Header: http.Header{},
	}

	result, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	body := rec.Body.String()
	require.Contains(t, body, "response.output_item.added")
	require.Contains(t, body, "response.completed")
}

// Content exceeding the window is released and streamed normally; a later EOF
// without a terminal event surfaces the incomplete error (cannot fail over once
// bytes were committed) but the content was delivered.
func TestOpenAIStreamingPassthrough_ShortStreamBuffer_ReleasesPastWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newShortStreamPassthroughService(16)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"type":"response.output_item.added","item":{"type":"message"},"output_index":0}`,
			"",
		}, "\n"))),
		Header: http.Header{},
	}

	_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing terminal event")
	require.Contains(t, rec.Body.String(), "response.output_item.added", "content past the window must be delivered")
}
