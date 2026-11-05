package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAntigravityStreamTerminal_UncommittedFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	thinking := agyStreamFrame(t, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"plan","thought":true}]}}],"usageMetadata":{"promptTokenCount":5,"thoughtsTokenCount":3}}}`)
	malformed := agyStreamFrame(t, `{"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","content":{"parts":[{"thoughtSignature":"sig"}]}}],"usageMetadata":{"promptTokenCount":5,"thoughtsTokenCount":1}}}`)

	for _, tc := range []struct {
		name      string
		body      string
		malformed bool
	}{
		{name: "thinking-only", body: thinking},
		{name: "malformed", body: malformed, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, caller := range agyStreamTerminalCallers() {
				t.Run(caller.name, func(t *testing.T) {
					result, body, err := caller.run(t, tc.body)
					require.Nil(t, result)
					var failover *UpstreamFailoverError
					require.ErrorAs(t, err, &failover)
					require.True(t, failover.RetryableOnSameAccount)
					require.NotContains(t, body, "end_turn")
					require.NotContains(t, body, "[DONE]")
					require.NotContains(t, body, "response.completed")
					require.NotContains(t, body, "plan")
					if tc.malformed {
						require.Contains(t, string(failover.ResponseBody), "MALFORMED_FUNCTION_CALL")
					}
				})
			}
		})
	}
}

func TestAntigravityStreamTerminal_VisibleOutputSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	text := agyStreamFrame(t, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}}`)
	tool := agyStreamFrame(t, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{}}}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}}`)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "text", body: text, want: "hello"},
		{name: "tool_use", body: tool, want: "read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, caller := range agyStreamTerminalCallers() {
				t.Run(caller.name, func(t *testing.T) {
					result, body, err := caller.run(t, tc.body)
					require.NotNil(t, result)
					require.NotNil(t, result.usage)
					require.Equal(t, 5, result.usage.InputTokens)
					var terminal *antigravityStreamTerminalError
					require.ErrorAs(t, err, &terminal)
					var failover *UpstreamFailoverError
					require.NotErrorAs(t, err, &failover)
					require.Contains(t, body, tc.want)
					require.Contains(t, body, terminal.reason)
					require.NotContains(t, body, "end_turn")
					require.NotContains(t, body, `"stop_reason":"tool_use"`)
					require.NotContains(t, body, "[DONE]")
					require.NotContains(t, body, "response.completed")
				})
			}
		})
	}
}

func TestAntigravityStreamTerminal_StopStillCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stop := agyStreamFrame(t, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}}`)
	thinkingStop := agyStreamFrame(t, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"plan","thought":true}]}}]}}`) +
		agyStreamFrame(t, `{"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"thoughtsTokenCount":3}}}`)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "STOP", body: stop, want: "hello"},
		{name: "thinking then STOP", body: thinkingStop, want: "plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, caller := range agyStreamTerminalCallers() {
				t.Run(caller.name, func(t *testing.T) {
					result, body, err := caller.run(t, tc.body)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Contains(t, body, tc.want)
					require.NotContains(t, body, "missing_finish_reason")
					require.NotContains(t, body, "malformed_function_call")
					switch caller.name {
					case "claude":
						require.Contains(t, body, `"stop_reason":"end_turn"`)
					case "chat":
						require.Contains(t, body, "[DONE]")
					default:
						require.Contains(t, body, "response.completed")
					}
				})
			}
		})
	}
}

func TestAntigravityStreamTerminal_KeepaliveWritesSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	thinking := agyStreamFrame(t, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"plan","thought":true}]}}],"usageMetadata":{"promptTokenCount":5,"thoughtsTokenCount":3}}}`)
	for _, tt := range []struct {
		name    string
		adapter antigravityCompatStreamAdapter
	}{
		{name: "chat", adapter: newAntigravityChatStreamAdapter("gemini-3.1-pro", false)},
		{name: "responses", adapter: newAntigravityResponsesStreamAdapter("gemini-3.1-pro")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, recorder := newAntigravityCompatContext(http.MethodPost, "/", nil)
			writer := newAntigravityClientWriter(c.Writer, c.Writer, "test")
			writer.beforeFirstWrite = func() { c.Header("Content-Type", "text/event-stream") }
			session := newAntigravityCompatStreamSession("gemini-3.1-pro", time.Now().Add(-time.Minute), tt.adapter, writer)
			session.writePreContentKeepalive(time.Now())
			require.True(t, session.preContentKeepaliveSent)
			session.consume(strings.TrimRight(thinking, "\n"))
			result, err := session.finish()
			require.NotNil(t, result)
			require.Equal(t, 3, result.usage.OutputTokens)
			var terminal *antigravityStreamTerminalError
			require.ErrorAs(t, err, &terminal)
			require.Equal(t, "missing_finish_reason", terminal.reason)
			var failover *UpstreamFailoverError
			require.NotErrorAs(t, err, &failover)
			body := recorder.Body.String()
			require.Contains(t, body, ": ping")
			require.Contains(t, body, "missing_finish_reason")
			require.Contains(t, body, "plan")
			require.NotContains(t, body, "end_turn")
			require.NotContains(t, body, "[DONE]")
			require.NotContains(t, body, "response.completed")
		})
	}
}

func TestAntigravityStreamTerminal_CompatUsageSurvivesError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, nil)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(agyStreamFrame(t, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}}`))),
	}
	result, err := svc.consumeAntigravityCompatResponse(context.Background(), c, nil, &antigravityCompatUpstreamCall{
		request: antigravityCompatRequest{
			protocol:      antigravityCompatChatCompletions,
			originalModel: "gemini-3.1-pro",
			clientStream:  true,
			includeUsage:  true,
			startTime:     time.Now(),
		},
		billingModel: "gemini-3.1-pro",
	}, resp)
	require.NotNil(t, result)
	require.Equal(t, 5, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)
	var terminal *antigravityStreamTerminalError
	require.ErrorAs(t, err, &terminal)
	require.NotContains(t, recorder.Body.String(), "[DONE]")
	require.NotContains(t, recorder.Body.String(), "end_turn")
	require.Contains(t, recorder.Body.String(), "hello")
}

type agyStreamTerminalCaller struct {
	name string
	run  func(t *testing.T, body string) (*antigravityStreamResult, string, error)
}

func agyStreamTerminalCallers() []agyStreamTerminalCaller {
	return []agyStreamTerminalCaller{
		{
			name: "claude",
			run: func(t *testing.T, body string) (*antigravityStreamResult, string, error) {
				t.Helper()
				svc := newAntigravityTestService(&config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}})
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/", nil)
				resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
				result, err := svc.handleClaudeStreamingResponse(c, resp, time.Now(), "claude-sonnet-4-5")
				return result, recorder.Body.String(), err
			},
		},
		{
			name: "chat",
			run: func(t *testing.T, body string) (*antigravityStreamResult, string, error) {
				t.Helper()
				svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, nil)
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", nil)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
				result, err := svc.handleChatCompletionsStreamingFromAntigravity(c, resp, time.Now(), "gemini-3.1-pro", true)
				return result, recorder.Body.String(), err
			},
		},
		{
			name: "responses",
			run: func(t *testing.T, body string) (*antigravityStreamResult, string, error) {
				t.Helper()
				svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, nil)
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/responses", nil)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
				result, err := svc.handleResponsesStreamingFromAntigravity(c, resp, time.Now(), "gemini-3.1-pro", apicompat.ResponsesClientToolMapping{})
				return result, recorder.Body.String(), err
			},
		},
	}
}

func agyStreamFrame(t *testing.T, body string) string {
	t.Helper()
	return "data: " + body + "\n\n"
}
