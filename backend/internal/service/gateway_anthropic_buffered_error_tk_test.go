//go:build unit

package service

// TK regression: a buffered Anthropic SSE stream that carries a terminal
// `error` event (HTTP 200 body, no message_start) must be attributed upstream.
//
// Before this fix apicompat.AnthropicStreamEvent silently dropped the error
// event, finalResp stayed nil, and all four buffered assembly paths fell through
// to a bare 502 "Upstream stream ended without a response" with no upstream
// context — which classifyOpsErrorLog then booked as
// error_phase=internal / error_owner=platform / error_source=gateway.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func anthropicErrorOnlySSE(errType, message string) string {
	return strings.Join([]string{
		"event: error",
		`data: {"type":"error","error":{"type":"` + errType + `","message":"` + message + `"}}`,
		"",
		"",
	}, "\n")
}

func newBufferedErrorUpstreamResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"x-request-id": []string{"rid_buffered_err"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func requireUpstreamAttributed(t *testing.T, c *gin.Context, wantUpstreamStatus int) {
	t.Helper()

	got, ok := c.Get(OpsUpstreamStatusCodeKey)
	require.True(t, ok, "upstream status must be recorded so ops classifies phase=upstream")
	require.Equal(t, wantUpstreamStatus, got)

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok, "an upstream error event must be appended")
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, wantUpstreamStatus, events[0].UpstreamStatusCode)
	require.NotEmpty(t, events[0].Message)

	// Recording the status is what flips ops classification away from
	// phase=internal / owner=platform. The classifier itself lives in package
	// handler, so the end-to-end assertion is in
	// handler/ops_error_logger_tk_buffered_upstream_test.go.
}

// requireTruncationCountsTowardsSLA pins the half of the truncation fix that
// upstream context cannot express on its own: with a 2xx wire status the ops
// logger only counts a request against SLA when an in-band failure is marked.
func requireTruncationCountsTowardsSLA(t *testing.T, c *gin.Context, wantIntendedStatus int) {
	t.Helper()

	streamErrs := GetOpsStreamErrors(c)
	require.Len(t, streamErrs, 1, "a truncated 200 must be marked as an in-band failure")
	require.True(t, streamErrs[0].CountTowardsSLA,
		"without CountTowardsSLA the truncated response is logged as recovered and escapes SLA")
	require.Equal(t, wantIntendedStatus, streamErrs[0].IntendedStatus)
	require.Equal(t, "upstream_stream_truncated", streamErrs[0].Code)
}

func TestTKAnthropicBufferedError_CCPathAttributesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := newBufferedErrorUpstreamResponse(
		anthropicErrorOnlySSE("rate_limit_error", "Number of concurrent connections exceeded"),
	)

	svc := &GatewayService{}
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-opus-4-8", nil, time.Now())
	require.Error(t, err)
	require.Nil(t, result)

	// Client sees the upstream reason and a retryable status, not an opaque 502.
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	body := rec.Body.String()
	require.Equal(t, "rate_limit_error", gjson.Get(body, "error.type").String())
	require.Contains(t, gjson.Get(body, "error.message").String(), "concurrent connections")

	requireUpstreamAttributed(t, c, http.StatusTooManyRequests)
}

func TestTKAnthropicBufferedError_ResponsesPathAttributesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := newBufferedErrorUpstreamResponse(
		anthropicErrorOnlySSE("overloaded_error", "Overloaded"),
	)

	svc := &GatewayService{}
	result, err := svc.handleResponsesBufferedStreamingResponse(
		resp, c, "gpt-5", "claude-opus-4-8", nil, time.Now(), apicompat.ResponsesClientToolMapping{},
	)
	require.Error(t, err)
	require.Nil(t, result)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, "overloaded_error", gjson.Get(rec.Body.String(), "error.code").String())

	requireUpstreamAttributed(t, c, statusAnthropicOverloaded)
}

func TestTKAnthropicBufferedError_NativeCCPathAttributesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := newBufferedErrorUpstreamResponse(
		anthropicErrorOnlySSE("invalid_request_error", "max_tokens is too large"),
	)

	svc := &OpenAIGatewayService{}
	result, err := svc.handleCCBufferedFromNativeAnthropic(
		resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now(),
	)
	require.Error(t, err)
	require.Nil(t, result)

	// A client-caused 400 stays a 400 rather than being inflated to 502.
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())

	requireUpstreamAttributed(t, c, http.StatusBadRequest)
}

func TestTKAnthropicBufferedError_NativeResponsesPathAttributesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := newBufferedErrorUpstreamResponse(
		anthropicErrorOnlySSE("authentication_error", "invalid x-api-key"),
	)

	svc := &OpenAIGatewayService{}
	result, err := svc.handleResponsesBufferedFromNativeAnthropic(
		resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now(), apicompat.ResponsesClientToolMapping{},
	)
	require.Error(t, err)
	require.Nil(t, result)

	// Upstream credential faults must not surface as a client 401.
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "upstream_error", gjson.Get(rec.Body.String(), "error.code").String())

	requireUpstreamAttributed(t, c, http.StatusUnauthorized)
}

// An empty-but-200 stream has no error event to parse. It is still an upstream
// protocol fault, so it must not be booked as a gateway-internal error either.
func TestTKAnthropicBufferedError_EmptyStreamStillAttributedUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := newBufferedErrorUpstreamResponse("event: ping\ndata: {\"type\":\"ping\"}\n\n")

	svc := &GatewayService{}
	_, err := svc.handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-opus-4-8", nil, time.Now())
	require.Error(t, err)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	// Message is preserved for operators who learned to grep for it.
	require.Contains(t, rec.Body.String(), "Upstream stream ended without a response")

	requireUpstreamAttributed(t, c, http.StatusBadGateway)

	raw, _ := c.Get(OpsUpstreamErrorsKey)
	events := raw.([]*OpsUpstreamErrorEvent)
	require.Equal(t, "stream_incomplete", events[0].Kind)
}

// A well-formed stream must not be disturbed by the new error-event branch.
func TestTKAnthropicBufferedError_HappyPathRecordsNoUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := newBufferedErrorUpstreamResponse(strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":11}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		"",
		"",
	}, "\n"))

	svc := &GatewayService{}
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-opus-4-8", nil, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)

	_, hasStatus := c.Get(OpsUpstreamStatusCodeKey)
	require.False(t, hasStatus)
	_, hasEvents := c.Get(OpsUpstreamErrorsKey)
	require.False(t, hasEvents)

	// A complete stream must never be marked as a truncated in-band failure;
	// otherwise the SLA assertions above could pass vacuously.
	require.Empty(t, GetOpsStreamErrors(c))
}

func TestTKAnthropicErrorTypeUpstreamStatusMapping(t *testing.T) {
	t.Parallel()

	for errType, want := range map[string]int{
		"invalid_request_error": http.StatusBadRequest,
		"authentication_error":  http.StatusUnauthorized,
		"permission_error":      http.StatusForbidden,
		"not_found_error":       http.StatusNotFound,
		"request_too_large":     http.StatusRequestEntityTooLarge,
		"rate_limit_error":      http.StatusTooManyRequests,
		"overloaded_error":      statusAnthropicOverloaded,
		"api_error":             http.StatusInternalServerError,
		// Types Anthropic does not put on the wire take the server-fault default
		// rather than each getting a speculative mapping.
		"billing_error":     http.StatusInternalServerError,
		"model_not_found":   http.StatusInternalServerError,
		"something_unknown": http.StatusInternalServerError,
	} {
		require.Equal(t, want, tkAnthropicErrorTypeUpstreamStatus(errType), errType)
	}
}

func TestTKParseAnthropicBufferedSSEError_IgnoresNonErrorEvents(t *testing.T) {
	t.Parallel()

	for _, payload := range []string{
		`{"type":"message_start","message":{"id":"m"}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}`,
		`{"type":"ping"}`,
		``,
	} {
		_, ok := tkParseAnthropicBufferedSSEError([]byte(payload), nil)
		require.False(t, ok, payload)
	}

	parsed, ok := tkParseAnthropicBufferedSSEError(
		[]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`), nil,
	)
	require.True(t, ok)
	require.Equal(t, "rate_limit_error", parsed.ErrType)
	require.Equal(t, "slow down", parsed.Message)
	require.False(t, parsed.EmptyStream)

	// A bare error event with no usable message still classifies.
	parsed, ok = tkParseAnthropicBufferedSSEError([]byte(`{"type":"error"}`), nil)
	require.True(t, ok)
	require.Equal(t, "api_error", parsed.ErrType)
	require.NotEmpty(t, parsed.Message)
}

// R-002: raw upstream payload capture must honor the operator's
// log_upstream_error_body setting, like every other appendOpsUpstreamError site.
func TestTKAnthropicBufferedError_DetailRespectsUpstreamBodyLogging(t *testing.T) {
	t.Parallel()

	const payload = `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`

	t.Run("nil cfg captures nothing", func(t *testing.T) {
		parsed, ok := tkParseAnthropicBufferedSSEError([]byte(payload), nil)
		require.True(t, ok)
		require.Empty(t, parsed.Detail)
	})

	t.Run("disabled captures nothing", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.LogUpstreamErrorBody = false
		parsed, ok := tkParseAnthropicBufferedSSEError([]byte(payload), cfg)
		require.True(t, ok)
		require.Empty(t, parsed.Detail)
	})

	t.Run("enabled captures truncated payload", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Gateway.LogUpstreamErrorBody = true
		cfg.Gateway.LogUpstreamErrorBodyMaxBytes = 16
		parsed, ok := tkParseAnthropicBufferedSSEError([]byte(payload), cfg)
		require.True(t, ok)
		require.Len(t, parsed.Detail, 16)
		require.Equal(t, payload[:16], parsed.Detail)
	})
}

// R-001: message_start (and content) followed by a terminal error event leaves
// finalResp non-nil. The partial content is still returned — those tokens were
// really produced — but the provider fault must reach ops instead of being
// booked as a clean success.
func TestTKAnthropicBufferedError_PartialContentThenErrorIsAttributed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := newBufferedErrorUpstreamResponse(strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial"}}`,
		"",
		"event: error",
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		"",
		"",
	}, "\n"))

	svc := &GatewayService{}
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, "gpt-5", "claude-opus-4-8", nil, time.Now())

	// The partial answer is preserved rather than discarded.
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "partial")

	// But the upstream fault is recorded, so ops does not see a clean success.
	requireUpstreamAttributed(t, c, statusAnthropicOverloaded)

	raw, _ := c.Get(OpsUpstreamErrorsKey)
	events := raw.([]*OpsUpstreamErrorEvent)
	require.Equal(t, "stream_truncated", events[0].Kind)
	require.Equal(t, "overloaded_error", events[0].Reason)

	// Upstream context alone would land in logOpsRecoveredUpstream, which keeps
	// recovered attempts out of request SLA. A truncated answer did not recover,
	// so it must also be marked as an in-band failure that counts.
	requireTruncationCountsTowardsSLA(t, c, http.StatusServiceUnavailable)
}

// The same partial-then-error shape on the CN native-Anthropic CC path.
func TestTKAnthropicBufferedError_NativePartialContentThenErrorIsAttributed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := newBufferedErrorUpstreamResponse(strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"glm-4.7","usage":{"input_tokens":10}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial"}}`,
		"",
		"event: error",
		`data: {"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
		"",
		"",
	}, "\n"))

	svc := &OpenAIGatewayService{}
	result, err := svc.handleCCBufferedFromNativeAnthropic(
		resp, c, "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now(),
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)

	requireUpstreamAttributed(t, c, http.StatusTooManyRequests)

	raw, _ := c.Get(OpsUpstreamErrorsKey)
	events := raw.([]*OpsUpstreamErrorEvent)
	require.Equal(t, "stream_truncated", events[0].Kind)

	requireTruncationCountsTowardsSLA(t, c, http.StatusTooManyRequests)
}
