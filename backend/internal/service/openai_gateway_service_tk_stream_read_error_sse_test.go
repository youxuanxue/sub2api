package service

import (
	"encoding/json"
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

// stepReadCloser returns the supplied chunks in order, then returns finalErr
// on every subsequent Read. It is used to simulate an upstream stream that
// successfully emits a few SSE bytes and then errors before sending its
// terminating blank line.
type stepReadCloser struct {
	chunks  [][]byte
	idx     int
	final   error
	yielded int
}

func (r *stepReadCloser) Read(p []byte) (int, error) {
	if r.idx < len(r.chunks) {
		chunk := r.chunks[r.idx]
		n := copy(p, chunk[r.yielded:])
		r.yielded += n
		if r.yielded >= len(chunk) {
			r.idx++
			r.yielded = 0
		}
		return n, nil
	}
	return 0, r.final
}

func (r *stepReadCloser) Close() error { return nil }

// TestOpenAIStreamingStreamReadErrorEmitsSeparateSSEEvents covers
// upstream Wei-Shaw/sub2api#1471: when an upstream read error fires after a
// `data:` line has been written but before its terminating blank line, the
// synthetic `stream_read_error` SSE event must start as its own event, not
// concatenate with the in-flight event's data field. Otherwise a single SSE
// event's data buffer contains two JSON objects joined by a newline, which
// the downstream OpenAI/Codex SDK parses as malformed JSON.
func TestOpenAIStreamingStreamReadErrorEmitsSeparateSSEEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: 0,
			StreamKeepaliveInterval:   0,
			MaxLineSize:               defaultMaxLineSize,
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	body := &stepReadCloser{
		chunks: [][]byte{
			// Preamble events so the gateway has initialized state. A
			// non-preamble event below will set clientOutputStarted=true and
			// route handleScanErr to sendErrorEvent (not pre-output failover).
			[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"),
			[]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_1\"}}\n\n"),
			// Non-preamble event that is intentionally NOT terminated by a
			// blank line. The next read returns io.ErrUnexpectedEOF before
			// the upstream could send the `\n\n` terminator.
			[]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n"),
		},
		final: io.ErrUnexpectedEOF,
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     http.Header{"X-Request-Id": []string{"rid-mid-stream"}},
	}

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}, time.Now(), "model", "model")
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream read error")

	out := rec.Body.String()
	require.Contains(t, out, "stream_read_error", "expected synthetic error SSE event to be emitted")
	require.Contains(t, out, "response.output_item.added", "expected the in-flight upstream event to still be visible")

	// Split into SSE events and assert each data line is independently valid JSON.
	for i, ev := range strings.Split(out, "\n\n") {
		ev = strings.TrimSpace(ev)
		if ev == "" {
			continue
		}
		var dataLines []string
		for _, line := range strings.Split(ev, "\n") {
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		require.Lenf(t, dataLines, 1, "event %d must carry exactly one data line, got %v (full event: %q)", i, dataLines, ev)
		var probe map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(dataLines[0]), &probe),
			"event %d data must be parseable JSON, got %q", i, dataLines[0])
	}
}
