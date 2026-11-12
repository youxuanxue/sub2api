package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAntigravityMalformedGenerationRetriesSameBodyThenSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{
		antigravitySSEResponse(antigravityMalformedFunctionCallSSE),
		antigravitySSEResponse(antigravityMalformedFunctionCallSSE),
		antigravitySSEResponse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}}` + "\n\n"),
	}}
	svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/messages", nil)

	result, err := svc.Forward(context.Background(), c, newAntigravityCompatAccount(AccountTypeOAuth), antigravityClaudeMessagesBody(true), false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 3, upstream.callCount)
	require.Equal(t, upstream.requestBodies[0], upstream.requestBodies[1])
	require.Equal(t, upstream.requestBodies[0], upstream.requestBodies[2])
	require.Contains(t, recorder.Body.String(), "hello")
	require.Contains(t, recorder.Body.String(), `"stop_reason":"end_turn"`)
}

func TestAntigravityMalformedGenerationExhaustedDoesNotSwitchAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{
		antigravitySSEResponse(antigravityMalformedFunctionCallSSE),
		antigravitySSEResponse(antigravityMalformedFunctionCallSSE),
		antigravitySSEResponse(antigravityMalformedFunctionCallSSE),
	}}
	svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/messages", nil)

	result, err := svc.Forward(context.Background(), c, newAntigravityCompatAccount(AccountTypeOAuth), antigravityClaudeMessagesBody(true), false)

	require.Nil(t, result)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.False(t, failover.ShouldRetryNextAccount())
	require.Equal(t, "malformed_function_call", failover.ClientErrorType)
	require.Equal(t, "upstream returned MALFORMED_FUNCTION_CALL", failover.ClientMessage)
	require.True(t, failover.RequestScopedTransient)
	require.Equal(t, 3, upstream.callCount)
	require.Equal(t, upstream.requestBodies[0], upstream.requestBodies[1])
	require.Equal(t, upstream.requestBodies[0], upstream.requestBodies[2])
	require.NotContains(t, recorder.Body.String(), "end_turn")
	require.NotContains(t, recorder.Body.String(), "tool_use")
}

func TestAntigravityMalformedGenerationVisibleOutputDoesNotRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}]}}` + "\n\n" +
		`data: {"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}}` + "\n\n"
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{
		antigravitySSEResponse(body),
		antigravitySSEResponse(antigravityMalformedFunctionCallSSE),
	}}
	svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/messages", nil)

	result, err := svc.Forward(context.Background(), c, newAntigravityCompatAccount(AccountTypeOAuth), antigravityClaudeMessagesBody(true), false)

	require.NotNil(t, result)
	var terminal *antigravityStreamTerminalError
	require.ErrorAs(t, err, &terminal)
	require.Equal(t, "malformed_function_call", terminal.reason)
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
	require.Equal(t, 1, upstream.callCount)
	require.Contains(t, recorder.Body.String(), "hello")
	require.NotContains(t, recorder.Body.String(), `"stop_reason":"end_turn"`)
}

func TestAntigravityMalformedGenerationNonStreamRetriesThenSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{
		antigravitySSEResponse(antigravityMalformedFunctionCallSSE),
		antigravitySSEResponse(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}}` + "\n\n"),
	}}
	svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/messages", nil)

	result, err := svc.Forward(context.Background(), c, newAntigravityCompatAccount(AccountTypeOAuth), antigravityClaudeMessagesBody(false), false)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.callCount)
	require.Equal(t, upstream.requestBodies[0], upstream.requestBodies[1])
	require.Contains(t, recorder.Body.String(), "hello")
	require.Contains(t, recorder.Body.String(), `"stop_reason":"end_turn"`)
}

const antigravityMalformedFunctionCallSSE = `data: {"response":{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","content":{"parts":[{"thoughtSignature":"sig"}]}}]}}` + "\n\n"

func antigravityClaudeMessagesBody(stream bool) []byte {
	if stream {
		return []byte(`{"model":"claude-sonnet-4-6","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	}
	return []byte(`{"model":"claude-sonnet-4-6","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
}

func antigravitySSEResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
