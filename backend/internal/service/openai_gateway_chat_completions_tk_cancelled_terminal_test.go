package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestForwardAsChatCompletions_ResponseCancelledIsTerminal_NoMissingTerminalError
// is the behaviour-contract regression for upstream Wei-Shaw/sub2api#1322.
// Before the fix, isOpenAICompatResponsesTerminalEvent only recognised
// completed/done/incomplete/failed; if the upstream Responses SSE ended with
// response.cancelled the stream loop fell through to [DONE], returning
// "stream usage incomplete: missing terminal event" and emitting a spurious
// openai.forward_failed ops_error_logs row. Helper-level unit coverage lives
// in openai_gateway_messages_tk_terminal_event_test.go; this test pins the
// cross-function contract (processDataLine -> finalizeStream) so future
// refactors of the streaming wiring cannot silently re-break it.
func TestForwardAsChatCompletions_ResponseCancelledIsTerminal_NoMissingTerminalError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4","status":"in_progress","output":[]}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
		`data: {"type":"response.cancelled","response":{"id":"resp_1","object":"response","model":"gpt-5.4","status":"cancelled","output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_cancelled"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          1,
		Name:        "openai-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.1")
	require.NoError(t, err, "response.cancelled must close the stream cleanly, not surface missing-terminal")
	require.NotNil(t, result)
	// Cancelled events carry usage in the upstream contract; billing must pick it up via the wrapper.
	require.Equal(t, 3, result.Usage.InputTokens)
	require.Equal(t, 1, result.Usage.OutputTokens)
	// Downstream client must see the canonical SSE terminal and no error tail.
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.NotContains(t, rec.Body.String(), "missing terminal event")
}

// TestForwardAsChatCompletions_ResponseCanceledIsTerminal_NoMissingTerminalError
// pins the same contract for the single-`l` "canceled" spelling. The OpenAI
// Responses contract uses both spellings interchangeably (see
// openai_ws_forwarder.go and openai_ws_v2/passthrough_relay.go), and any
// future trimming that drops one but not the other re-opens #1322 silently.
func TestForwardAsChatCompletions_ResponseCanceledIsTerminal_NoMissingTerminalError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstreamBody := strings.Join([]string{
		`data: {"type":"response.canceled","response":{"id":"resp_2","object":"response","model":"gpt-5.4","status":"canceled","output":[],"usage":{"input_tokens":2,"output_tokens":0,"total_tokens":2}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "x-request-id": []string{"rid_chat_canceled"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          1,
		Name:        "openai-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-acc",
		},
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "gpt-5.1")
	require.NoError(t, err, "response.canceled must close the stream cleanly, not surface missing-terminal")
	require.NotNil(t, result)
	require.Equal(t, 2, result.Usage.InputTokens)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.NotContains(t, rec.Body.String(), "missing terminal event")
}
