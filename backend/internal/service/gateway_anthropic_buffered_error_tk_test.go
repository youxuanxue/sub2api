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
	"errors"
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
)

var errBufferedAnthropicTestRead = errors.New("buffered anthropic test read failure")

type errAfterPayloadReadCloser struct {
	reader *strings.Reader
}

func (r *errAfterPayloadReadCloser) Read(p []byte) (int, error) {
	if r.reader.Len() == 0 {
		return 0, errBufferedAnthropicTestRead
	}
	return r.reader.Read(p)
}

func (r *errAfterPayloadReadCloser) Close() error { return nil }

func anthropicMessageStartOnlySSE() string {
	return strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10}}}`,
		"",
		"",
	}, "\n")
}

func anthropicMessageStartThenErrorSSE(errType, message string) string {
	return strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10}}}`,
		"",
		"event: error",
		`data: {"type":"error","error":{"type":"` + errType + `","message":"` + message + `"}}`,
		"",
		"",
	}, "\n")
}

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

func newBufferedReadErrorUpstreamResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"x-request-id": []string{"rid_buffered_read_err"}},
		Body:       &errAfterPayloadReadCloser{reader: strings.NewReader(body)},
	}
}

func bufferedAnthropicTestAccount(platform string) *Account {
	return &Account{ID: 1805, Name: "buffered-anthropic-test", Platform: platform}
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
	account := bufferedAnthropicTestAccount(PlatformAnthropic)
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, account, "gpt-5", "claude-opus-4-8", nil, time.Now())
	require.Error(t, err)
	require.Nil(t, result)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Equal(t, "rate_limit_error", failoverErr.ClientErrorType)
	require.Contains(t, failoverErr.ClientMessage, "concurrent connections")
	require.False(t, c.Writer.Written())

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
	account := bufferedAnthropicTestAccount(PlatformAnthropic)
	result, err := svc.handleResponsesBufferedStreamingResponse(
		resp, c, account, "gpt-5", "claude-opus-4-8", nil, time.Now(), apicompat.ResponsesClientToolMapping{},
	)
	require.Error(t, err)
	require.Nil(t, result)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.ClientStatusCode)
	require.Equal(t, "overloaded_error", failoverErr.ClientErrorType)
	require.False(t, c.Writer.Written())

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
	account := bufferedAnthropicTestAccount(PlatformOpenAI)
	result, err := svc.handleCCBufferedFromNativeAnthropic(
		resp, c, account, "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now(),
	)
	require.Error(t, err)
	require.Nil(t, result)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadRequest, failoverErr.ClientStatusCode)
	require.Equal(t, "invalid_request_error", failoverErr.ClientErrorType)
	require.False(t, c.Writer.Written())

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
	account := bufferedAnthropicTestAccount(PlatformOpenAI)
	result, err := svc.handleResponsesBufferedFromNativeAnthropic(
		resp, c, account, "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now(), apicompat.ResponsesClientToolMapping{},
	)
	require.Error(t, err)
	require.Nil(t, result)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.ClientStatusCode)
	require.Equal(t, "upstream_error", failoverErr.ClientErrorType)
	require.False(t, c.Writer.Written())

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
	account := bufferedAnthropicTestAccount(PlatformAnthropic)
	_, err := svc.handleCCBufferedFromAnthropic(resp, c, account, "gpt-5", "claude-opus-4-8", nil, time.Now())
	require.Error(t, err)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.ClientStatusCode)
	require.False(t, c.Writer.Written())

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
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, bufferedAnthropicTestAccount(PlatformAnthropic), "gpt-5", "claude-opus-4-8", nil, time.Now())
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
	require.Equal(t, "stream_error", parsed.Kind)

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
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, bufferedAnthropicTestAccount(PlatformAnthropic), "gpt-5", "claude-opus-4-8", nil, time.Now())

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
		resp, c, bufferedAnthropicTestAccount(PlatformOpenAI), "glm-4.7", "glm-4.7", "glm-4.7", nil, time.Now(),
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

func TestUS047_MessageStartThenErrorReturnsFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	resp := newBufferedErrorUpstreamResponse(
		anthropicMessageStartThenErrorSSE("rate_limit_error", "slow down"),
	)

	svc := &GatewayService{}
	account := bufferedAnthropicTestAccount(PlatformAnthropic)
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, account, "gpt-5", "claude-opus-4-8", nil, time.Now())

	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.ClientStatusCode)
	require.False(t, c.Writer.Written(), "service must not commit a response before failover is exhausted")

	raw, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events := raw.([]*OpsUpstreamErrorEvent)
	require.Len(t, events, 1)
	require.Equal(t, account.Platform, events[0].Platform)
	require.Equal(t, account.ID, events[0].AccountID)
	require.Equal(t, account.Name, events[0].AccountName)
}

func TestUS047_PartialContentThenErrorPreservesResponse(t *testing.T) {
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
	result, err := svc.handleCCBufferedFromAnthropic(resp, c, bufferedAnthropicTestAccount(PlatformAnthropic), "gpt-5", "claude-opus-4-8", nil, time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "partial")
	requireTruncationCountsTowardsSLA(t, c, http.StatusServiceUnavailable)
}

func TestUS047_IncompleteEOFRoutesByContentAvailability(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("message_start only fails over", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		resp := newBufferedErrorUpstreamResponse(anthropicMessageStartOnlySSE())

		result, err := (&GatewayService{}).handleCCBufferedFromAnthropic(
			resp, c, bufferedAnthropicTestAccount(PlatformAnthropic), "gpt-5", "claude-opus-4-8", nil, time.Now(),
		)

		require.Nil(t, result)
		var failoverErr *UpstreamFailoverError
		require.True(t, errors.As(err, &failoverErr))
		require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
		require.False(t, c.Writer.Written())
	})

	t.Run("content before EOF is preserved and marked", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		resp := newBufferedErrorUpstreamResponse(strings.Join([]string{
			"event: message_start",
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10}}}`,
			"",
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial"}}`,
			"",
			"",
		}, "\n"))

		result, err := (&GatewayService{}).handleCCBufferedFromAnthropic(
			resp, c, bufferedAnthropicTestAccount(PlatformAnthropic), "gpt-5", "claude-opus-4-8", nil, time.Now(),
		)

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "partial")
		requireTruncationCountsTowardsSLA(t, c, http.StatusBadGateway)
	})
}

func TestUS047_ScannerReadErrorRoutesByContentAvailability(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("message_start only fails over", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		resp := newBufferedReadErrorUpstreamResponse(anthropicMessageStartOnlySSE())

		result, err := (&GatewayService{}).handleCCBufferedFromAnthropic(
			resp, c, bufferedAnthropicTestAccount(PlatformAnthropic), "gpt-5", "claude-opus-4-8", nil, time.Now(),
		)

		require.Nil(t, result)
		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr)
		require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
		require.Contains(t, failoverErr.ClientMessage, "read failed")
		require.False(t, c.Writer.Written())

		raw, ok := c.Get(OpsUpstreamErrorsKey)
		require.True(t, ok)
		events := raw.([]*OpsUpstreamErrorEvent)
		require.Len(t, events, 1)
		require.Equal(t, "stream_read_error", events[0].Kind)
	})

	t.Run("content before read error is preserved and marked", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		resp := newBufferedReadErrorUpstreamResponse(strings.Join([]string{
			"event: message_start",
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10}}}`,
			"",
			"event: content_block_start",
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial"}}`,
			"",
			"",
		}, "\n"))

		result, err := (&GatewayService{}).handleCCBufferedFromAnthropic(
			resp, c, bufferedAnthropicTestAccount(PlatformAnthropic), "gpt-5", "claude-opus-4-8", nil, time.Now(),
		)

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "partial")
		requireTruncationCountsTowardsSLA(t, c, http.StatusBadGateway)

		raw, ok := c.Get(OpsUpstreamErrorsKey)
		require.True(t, ok)
		events := raw.([]*OpsUpstreamErrorEvent)
		require.Len(t, events, 1)
		require.Equal(t, "stream_truncated", events[0].Kind)
	})
}

func TestUS047_CompletionBeforeReadErrorRemainsSuccessful(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	resp := newBufferedReadErrorUpstreamResponse(strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10}}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"complete"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		"",
		"",
	}, "\n"))

	result, err := (&GatewayService{}).handleCCBufferedFromAnthropic(
		resp, c, bufferedAnthropicTestAccount(PlatformAnthropic), "gpt-5", "claude-opus-4-8", nil, time.Now(),
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "complete")
	require.Empty(t, GetOpsStreamErrors(c))
	_, hasUpstreamEvents := c.Get(OpsUpstreamErrorsKey)
	require.False(t, hasUpstreamEvents)
}

func TestUS047_ClientErrorTypeMapping(t *testing.T) {
	t.Parallel()

	require.Equal(t, "not_found_error", tkAnthropicBufferedClientErrType("not_found_error"))
	require.Equal(t, "request_too_large", tkAnthropicBufferedClientErrType("request_too_large"))
}
