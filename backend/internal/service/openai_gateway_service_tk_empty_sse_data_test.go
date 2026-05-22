package service

import (
	"bytes"
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

// TestOpenAISSEDataPayloadIsEmpty covers the helper predicate driving the
// upstream Wei-Shaw/sub2api#2298 fix: empty / whitespace-only `data:` SSE
// payloads must be reported as empty so the three forward paths drop them
// before they reach the client.
func TestOpenAISSEDataPayloadIsEmpty(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		{"empty string", "", true},
		{"single space", " ", true},
		{"tabs and newlines", "\t\n  \r", true},
		{"normal JSON object", `{"type":"response.created"}`, false},
		{"DONE sentinel", "[DONE]", false},
		{"JSON with leading space", ` {"a":1}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := openAISSEDataPayloadIsEmpty(tc.data)
			require.Equal(t, tc.want, got)
		})
	}
}

// fullReadCloser returns the supplied body on a single Read and then EOF.
type fullReadCloser struct {
	r *bytes.Reader
}

func (f *fullReadCloser) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *fullReadCloser) Close() error               { return nil }

func newFullReadCloser(body string) io.ReadCloser {
	return &fullReadCloser{r: bytes.NewReader([]byte(body))}
}

// streamHasEmptyDataLine reports whether any non-comment line in the captured
// SSE transcript is exactly `data:` or `data:` followed by only whitespace.
// The OpenAI Python SDK calls json.loads() on the bytes after `data:` and
// crashes on empty input; this predicate is the load-bearing invariant
// upstream Wei-Shaw/sub2api#2298 asks the proxy to enforce.
func streamHasEmptyDataLine(t *testing.T, out string) bool {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if rest == "" {
			return true
		}
	}
	return false
}

// dataPayloadsInSSE collects the trimmed payload of every `data:` line in the
// captured SSE transcript. Used to assert the non-empty events still reach
// the client unchanged.
func dataPayloadsInSSE(out string) []string {
	var payloads []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if rest != "" {
			payloads = append(payloads, rest)
		}
	}
	return payloads
}

// TestOpenAIStreamingDropsEmptySSEDataFrames exercises the synchronous
// scanner path of handleStreamingResponse with an upstream that emits
// interleaved valid events and the bare `data:\n\n` / `data: \n\n` frames
// gpt-5.5 is observed sending on `/v1/responses`. Without the
// openAISSEDataPayloadIsEmpty guard added for upstream Wei-Shaw/sub2api#2298,
// those empty frames are forwarded to the client and break OpenAI-SDK
// consumers at the next json.loads() call.
func TestOpenAIStreamingDropsEmptySSEDataFrames(t *testing.T) {
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

	// Upstream transcript shape observed on gpt-5.5 /v1/responses: valid
	// events interleaved with bare empty data frames (`data:\n\n` and
	// `data: \n\n` are both reported in the wild) plus a final terminal
	// event.
	body := newFullReadCloser(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		``,
		`data:`,
		``,
		`data: {"type":"response.in_progress","response":{"id":"resp_1"}}`,
		``,
		`data: `,
		``,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
		``,
		`data:    `,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":5}}}`,
		``,
	}, "\n"))

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     http.Header{"X-Request-Id": []string{"rid-empty-frames"}},
	}

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}, time.Now(), "model", "model")
	require.NoError(t, err)

	out := rec.Body.String()

	require.Falsef(t, streamHasEmptyDataLine(t, out),
		"empty `data:` SSE frame leaked to client (would crash OpenAI Python SDK on json.loads()): transcript=%q", out)

	payloads := dataPayloadsInSSE(out)
	require.Lenf(t, payloads, 4, "expected 4 non-empty data: payloads (created, in_progress, output_item.added, completed); got %d (%v)", len(payloads), payloads)

	// Sanity-check each surviving payload is independently parseable JSON
	// and carries its original type field. This guarantees the empty-frame
	// guard does not corrupt or merge neighboring events.
	wantTypes := []string{"response.created", "response.in_progress", "response.output_item.added", "response.completed"}
	for i, payload := range payloads {
		var probe map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(payload), &probe),
			"data payload %d must be parseable JSON, got %q", i, payload)
		require.Equalf(t, wantTypes[i], probe["type"], "event %d type mismatch (full payloads=%v)", i, payloads)
	}
}

// TestOpenAIChatCompletionsRawDropsEmptySSEDataFrames exercises the raw
// Chat Completions forward path (streamRawChatCompletions) used for third
// party OpenAI-compatible upstreams (DeepSeek / Kimi / GLM / Qwen ...).
// Same upstream Wei-Shaw/sub2api#2298 invariant: bare empty `data:` frames
// must be dropped before reaching the client.
func TestOpenAIChatCompletionsRawDropsEmptySSEDataFrames(t *testing.T) {
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

	// Chat Completions SSE shape: each chunk is a Chat Completions delta
	// with at least one choices[*].delta.content token so the silent
	// refusal detector releases the client output buffer.
	body := newFullReadCloser(strings.Join([]string{
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		``,
		`data:`,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" there"}}]}`,
		``,
		`data:  `,
		``,
		`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n"))

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     http.Header{"X-Request-Id": []string{"rid-cc-raw-empty"}},
	}

	_, err := svc.streamRawChatCompletions(c, resp, &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}, "deepseek-chat", "deepseek-chat", "deepseek-chat", nil, nil, time.Now(), 0)
	require.NoError(t, err)

	out := rec.Body.String()

	require.Falsef(t, streamHasEmptyDataLine(t, out),
		"empty `data:` SSE frame leaked to client through chat_completions raw path: transcript=%q", out)

	payloads := dataPayloadsInSSE(out)
	// 3 chunks + [DONE] = 4 surviving data lines.
	require.Lenf(t, payloads, 4, "expected 3 chat chunks + [DONE] payloads; got %d (%v)", len(payloads), payloads)
	require.Equal(t, "[DONE]", payloads[len(payloads)-1], "[DONE] sentinel must survive the empty-frame filter")
}

// TestOpenAIPassthroughDropsEmptySSEDataFrames exercises the OAuth
// passthrough streaming forward path (handleStreamingResponsePassthrough).
// Same upstream Wei-Shaw/sub2api#2298 invariant: bare empty `data:` frames
// must be dropped before reaching the client.
func TestOpenAIPassthroughDropsEmptySSEDataFrames(t *testing.T) {
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

	body := newFullReadCloser(strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}`,
		``,
		`data:`,
		``,
		`data: {"type":"response.in_progress","response":{"id":"resp_1"}}`,
		``,
		`data: `,
		``,
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":3,"output_tokens":5}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n"))

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     http.Header{"X-Request-Id": []string{"rid-passthrough-empty"}},
	}

	_, err := svc.handleStreamingResponsePassthrough(c.Request.Context(), resp, c, &Account{ID: 1, Platform: PlatformOpenAI, Name: "acc"}, time.Now(), "model", "model")
	require.NoError(t, err)

	out := rec.Body.String()

	require.Falsef(t, streamHasEmptyDataLine(t, out),
		"empty `data:` SSE frame leaked to client through passthrough path: transcript=%q", out)

	payloads := dataPayloadsInSSE(out)
	// 3 real events + the [DONE] sentinel = 4 surviving data lines.
	require.Lenf(t, payloads, 4, "expected 3 events + [DONE] payloads on passthrough path; got %d (%v)", len(payloads), payloads)
	require.Equal(t, "[DONE]", payloads[len(payloads)-1], "[DONE] sentinel must survive the empty-frame filter")
}
