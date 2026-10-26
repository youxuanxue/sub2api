//go:build unit

package service

import (
	"context"
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

// geminiNativeTruncationCase 是守卫的双向矩阵：每个形状要么必须被判为截断，
// 要么必须完全静默通过。MAX_TOKENS / SAFETY 是模型侧生成结果，不是上游故障。
type geminiNativeTruncationCase struct {
	name string
	// wire 是上游 SSE 字节（干净 EOF，无传输层错误）。
	wire string
	// wantTruncated 为 true 时守卫必须报错并记 stream_truncated。
	wantTruncated bool
	// wantOpsEvent 为 false 时守卫不得新增任何上游错误事件（空流由
	// finalizeGeminiSSESignal 自己登记，与本守卫无关）。
	wantOpsEvent bool
}

func geminiNativeTruncationCases() []geminiNativeTruncationCase {
	const chunk = `data: {"candidates":[{"content":{"parts":[{"text":"half an answer"}]}}],"usageMetadata":{"promptTokenCount":12}}` + "\n\n"
	return []geminiNativeTruncationCase{
		{
			name:          "truncated_clean_eof_without_finish_reason",
			wire:          chunk + chunk,
			wantTruncated: true,
			wantOpsEvent:  true,
		},
		{
			name: "complete_stop_is_silent",
			wire: chunk + `data: {"candidates":[{"content":{"parts":[{"text":"."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}` + "\n\n",
		},
		{
			// MAX_TOKENS 是模型生成结果：守卫必须静默，不得换号。
			name: "max_tokens_is_silent",
			wire: chunk + `data: {"candidates":[{"content":{"parts":[{"text":"."}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5}}` + "\n\n",
		},
		{
			// SAFETY 已由 gemini_response_signal.go 按请求级内容过滤记账。
			name: "safety_is_silent",
			wire: chunk + `data: {"candidates":[{"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":12}}` + "\n\n",
		},
		{
			name: "prompt_blocked_is_silent",
			wire: chunk + `data: {"promptFeedback":{"blockReason":"SAFETY"}}` + "\n\n",
		},
		{
			name: "error_envelope_is_silent",
			wire: chunk + `data: {"error":{"code":503,"status":"UNAVAILABLE","message":"overloaded"}}` + "\n\n",
		},
		{
			name: "done_sentinel_is_silent",
			wire: chunk + "data: [DONE]\n\n",
		},
		{
			// 零 data 事件的空流由 finalizeGeminiSSESignal 登记 EMPTY_STREAM；
			// 本守卫不得改变其行为，也不得叠加第二条错误。
			name: "empty_stream_keeps_existing_behavior",
			wire: "\n\n",
		},
	}
}

func geminiNativeTruncationRequire(t *testing.T, c *gin.Context, tc geminiNativeTruncationCase, err error) {
	t.Helper()
	truncatedEvents := 0
	if raw, ok := c.Get(OpsUpstreamErrorsKey); ok {
		events, _ := raw.([]*OpsUpstreamErrorEvent)
		for _, ev := range events {
			if ev != nil && ev.Kind == "stream_truncated" {
				truncatedEvents++
			}
		}
	}

	if !tc.wantTruncated {
		require.NoError(t, err, "%s must pass silently", tc.name)
		require.Zero(t, truncatedEvents, "%s must not record stream_truncated", tc.name)
		return
	}

	require.ErrorIs(t, err, ErrGeminiNativeStreamTruncated)
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr,
		"bytes already reached the client: replay would duplicate content")
	require.Equal(t, 1, truncatedEvents, "exactly one stream_truncated event")

	streamErrs := GetOpsStreamErrors(c)
	require.NotEmpty(t, streamErrs)
	last := streamErrs[len(streamErrs)-1]
	require.Equal(t, GeminiNativeStreamTruncatedCode, last.Code)
	require.True(t, last.CountTowardsSLA, "truncated answers must count towards SLA")
	require.Equal(t, http.StatusBadGateway, last.IntendedStatus)
}

// TestGeminiNativeStreamTruncationGuard covers the Gemini native passthrough
// (streamGenerateContent?alt=sse) in both directions: a clean EOF before any
// terminal event must fail, every legitimate terminal shape must stay silent.
func TestGeminiNativeStreamTruncationGuard(t *testing.T) {
	for _, tc := range geminiNativeTruncationCases() {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost,
				"/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse", nil)

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tc.wire)),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			}
			svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
			account := &Account{ID: 42, Name: "gemini-acct", Platform: PlatformGemini}

			_, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, account, "req-1", nil, nil)
			geminiNativeTruncationRequire(t, c, tc, err)
		})
	}
}

// TestAntigravityGeminiStreamTruncationGuard covers the Antigravity native
// Gemini passthrough, which shares the same wire format and the same bug.
func TestAntigravityGeminiStreamTruncationGuard(t *testing.T) {
	for _, tc := range geminiNativeTruncationCases() {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost,
				"/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse", nil)

			resp := &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(tc.wire)),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			}
			svc := newAntigravityTestService(&config.Config{
				Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize},
			})
			account := &Account{ID: 43, Name: "antigravity-acct", Platform: PlatformAntigravity}

			streamRes, err := svc.handleGeminiStreamingResponse(c, resp, time.Now())
			if err == nil {
				err = finalizeAntigravityGeminiStreamTruncation(c, streamRes, account, "req-2")
			}
			geminiNativeTruncationRequire(t, c, tc, err)
		})
	}
}

// Client-side cancellation cancels the upstream request too, so its EOF is
// indistinguishable from a real truncation. Keep the existing semantics: bill
// the collected usage, do not blame the account.
func TestGeminiNativeStreamTruncation_ClientAbortIsNotUpstreamFault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost,
		"/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse", nil).WithContext(ctx)

	wire := `data: {"candidates":[{"content":{"parts":[{"text":"half"}]}}]}` + "\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(wire)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
	svc := &GeminiMessagesCompatService{cfg: &config.Config{}}
	_, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false,
		&Account{ID: 44, Name: "acct", Platform: PlatformGemini}, "req-3", nil, nil)
	require.NoError(t, err, "client abort must not be attributed to the upstream account")
}
