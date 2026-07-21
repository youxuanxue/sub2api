//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// ---- IsKiro / typed getters / toKiroProtoAccount ----

func TestAccount_IsKiro(t *testing.T) {
	require.True(t, (&Account{Platform: PlatformKiro}).IsKiro())
	require.False(t, (&Account{Platform: PlatformAnthropic}).IsKiro())
	require.False(t, (&Account{Platform: PlatformOpenAI}).IsKiro())
}

func TestAccount_ToKiroProtoAccount(t *testing.T) {
	acct := &Account{
		ID:       42,
		Platform: PlatformKiro,
		Credentials: map[string]any{
			"access_token":  "at",
			"refresh_token": "rt",
			"profile_arn":   "arn:profile",
			"region":        "eu-west-1",
			"machine_id":    "m1",
			"auth_method":   "idc",
			"client_id":     "cid",
			"client_secret": "csecret",
		},
	}
	pa := acct.toKiroProtoAccount()
	require.NotNil(t, pa)
	require.Equal(t, "42", pa.ID) // ID is string
	require.Equal(t, "at", pa.AccessToken)
	require.Equal(t, "rt", pa.RefreshToken)
	require.Equal(t, "arn:profile", pa.ProfileArn)
	require.Equal(t, "eu-west-1", pa.Region)
	require.Equal(t, "m1", pa.MachineId)
	require.Equal(t, "idc", pa.AuthMethod)
	require.Equal(t, "cid", pa.ClientID)
	require.Equal(t, "csecret", pa.ClientSecret)
	require.True(t, pa.Enabled)
}

func TestAccount_ToKiroProtoAccount_RegionDefault(t *testing.T) {
	acct := &Account{ID: 7, Platform: PlatformKiro}
	pa := acct.toKiroProtoAccount()
	require.Equal(t, kiroDefaultRegion, pa.Region)
	require.Equal(t, "us-east-1", pa.Region)
}

// ---- Forward (fake HTTPDoer + canned EventStream) ----

// buildKiroEventStreamMessage hand-assembles a single AWS Event Stream binary
// frame (one String header `:event-type` + JSON payload) matching the byte
// layout decoded by the vendored parseEventStream. Mirrors the helper in
// internal/integration/kiro/eventstream_test.go (cannot be imported across
// package test boundaries).
func buildKiroEventStreamMessage(eventType string, payload []byte) []byte {
	const headerName = ":event-type"
	var headers bytes.Buffer
	headers.WriteByte(byte(len(headerName)))
	headers.WriteString(headerName)
	headers.WriteByte(7) // String
	var vlen [2]byte
	binary.BigEndian.PutUint16(vlen[:], uint16(len(eventType)))
	headers.Write(vlen[:])
	headers.WriteString(eventType)
	return buildKiroEventStreamFrame(headers.Bytes(), payload)
}

func buildKiroEventStreamException(exceptionType string, payload []byte) []byte {
	var headers bytes.Buffer
	writeStringHeader := func(name, value string) {
		headers.WriteByte(byte(len(name)))
		headers.WriteString(name)
		headers.WriteByte(7)
		var vlen [2]byte
		binary.BigEndian.PutUint16(vlen[:], uint16(len(value)))
		headers.Write(vlen[:])
		headers.WriteString(value)
	}
	writeStringHeader(":message-type", "exception")
	writeStringHeader(":exception-type", exceptionType)
	return buildKiroEventStreamFrame(headers.Bytes(), payload)
}

func buildKiroEventStreamFrame(headerBytes, payload []byte) []byte {

	headersLen := len(headerBytes)
	totalLen := 12 + headersLen + len(payload) + 4

	var frame bytes.Buffer
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], uint32(totalLen))
	frame.Write(u32[:])
	binary.BigEndian.PutUint32(u32[:], uint32(headersLen))
	frame.Write(u32[:])
	frame.Write([]byte{0, 0, 0, 0}) // prelude CRC (unchecked)
	frame.Write(headerBytes)
	frame.Write(payload)
	frame.Write([]byte{0, 0, 0, 0}) // message CRC (unchecked)
	return frame.Bytes()
}

// kiroFakeUpstream returns a canned 200 EventStream response.
type kiroFakeUpstream struct {
	body       []byte
	sawTLS     bool
	gotRequest bool
}

type kiroSequenceUpstream struct {
	bodies [][]byte
	calls  int
}

func (u *kiroSequenceUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected Do call")
}

func (u *kiroSequenceUpstream) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	index := u.calls
	u.calls++
	if index >= len(u.bodies) {
		index = len(u.bodies) - 1
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(u.bodies[index])),
	}, nil
}

func (u *kiroFakeUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected Do call")
}

func (u *kiroFakeUpstream) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.gotRequest = true
	u.sawTLS = profile != nil
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(u.body)),
	}
	return resp, nil
}

func newKiroAccountForTest() *Account {
	return &Account{
		ID:       99,
		Platform: PlatformKiro,
		Credentials: map[string]any{
			"access_token":  "at",
			"refresh_token": "rt",
			"profile_arn":   "arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
			"region":        "us-east-1",
			"auth_method":   "social",
		},
	}
}

func TestKiroGatewayService_Forward_NonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"hello world","inputTokens":12,"outputTokens":5}`))
	upstream := &kiroFakeUpstream{body: frame}

	svc := NewKiroGatewayService(upstream, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 16,
		"stream":     false,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4", Stream: false}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, upstream.gotRequest)
	require.False(t, result.Stream)
	// Kiro upstream reports credits only (never tokens); usage is estimated
	// locally from request/response content, so token counts must be positive
	// and the billing tier marked as estimated.
	require.Positive(t, result.Usage.InputTokens)
	require.Positive(t, result.Usage.OutputTokens)
	require.Equal(t, "kiro-estimated", result.BillingTier)
	require.Equal(t, "claude-sonnet-4", result.Model)
	require.NotEmpty(t, result.RequestID)

	// Response body is an Anthropic Messages JSON envelope with the text.
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "message", resp["type"])
	require.Equal(t, result.RequestID, resp["id"])
}

func TestKiroGatewayService_Forward_EmptyResponseTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	svc := NewKiroGatewayService(&kiroFakeUpstream{}, nil, nil)
	body, _ := json.Marshal(map[string]any{
		"model":    "claude-sonnet-4",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   false,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4", Stream: false}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "response_error", events[0].Kind)
	require.Equal(t, "empty_response", events[0].Reason)
	require.Equal(t, "kiro upstream returned an empty response", events[0].Message)
	require.Equal(t, int64(99), events[0].AccountID)
}

func kiroContentFilteredEventStream() []byte {
	var stream []byte
	stream = append(stream, buildKiroEventStreamMessage("metadataEvent", []byte(`{"stopReason":"CONTENT_FILTERED"}`))...)
	stream = append(stream, buildKiroEventStreamMessage("contextUsageEvent", []byte(`{"contextUsagePercentage":0.01}`))...)
	stream = append(stream, buildKiroEventStreamMessage("meteringEvent", []byte(`{"usage":1}`))...)
	return stream
}

func newKiroParsedRequestForTest(stream bool) *ParsedRequest {
	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-5",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 16,
		"stream":     stream,
	})
	return &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5", Stream: stream}
}

func requireKiroContentFilteredError(t *testing.T, c *gin.Context, rec *httptest.ResponseRecorder, result *ForwardResult, err error) {
	t.Helper()
	require.Nil(t, result)
	var contentFilteredErr *KiroContentFilteredError
	require.ErrorAs(t, err, &contentFilteredErr)
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Empty(t, rec.Body.String())

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, http.StatusOK, events[0].UpstreamStatusCode)
	require.Equal(t, "policy_error", events[0].Kind)
	require.Equal(t, KiroContentFilteredOutcome, events[0].Reason)
}

func TestKiroGatewayService_Forward_NonStreaming_ContentFilteredIsNotFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	svc := NewKiroGatewayService(&kiroFakeUpstream{body: kiroContentFilteredEventStream()}, nil, nil)

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(false), time.Now())
	requireKiroContentFilteredError(t, c, rec, result, err)
}

func TestKiroGatewayService_Forward_Streaming_ContentFilteredIsNotFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	svc := NewKiroGatewayService(&kiroFakeUpstream{body: kiroContentFilteredEventStream()}, nil, nil)

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(true), time.Now())
	requireKiroContentFilteredError(t, c, rec, result, err)
}

func TestKiroGatewayService_Forward_ContentFilteredWithAssistantTextRemainsSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	stream := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"I cannot help with that request."}`))
	stream = append(stream, kiroContentFilteredEventStream()...)
	svc := NewKiroGatewayService(&kiroFakeUpstream{body: stream}, nil, nil)

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(false), time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "I cannot help with that request.")
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded)
}

func TestKiroGatewayService_Forward_NonStreaming_ReadFailureRetriesWithoutPartialOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	partial := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"discard me"}`))
	partial = append(partial, []byte{0, 0, 0, 20}...)
	recovered := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"recovered"}`))
	upstream := &kiroSequenceUpstream{bodies: [][]byte{partial, recovered}}
	svc := NewKiroGatewayService(upstream, nil, nil)
	body, _ := json.Marshal(map[string]any{
		"model":      "claude-opus-4-8",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 16,
		"stream":     false,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-8", Stream: false}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.calls)
	require.Contains(t, rec.Body.String(), "recovered")
	require.NotContains(t, rec.Body.String(), "discard me")
}

func TestKiroGatewayService_Forward_Streaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"hi there","inputTokens":8,"outputTokens":3}`))
	upstream := &kiroFakeUpstream{body: frame}

	svc := NewKiroGatewayService(upstream, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 16,
		"stream":     true,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4", Stream: true}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	// Estimated usage (Kiro upstream reports credits only).
	require.Positive(t, result.Usage.InputTokens)
	require.Positive(t, result.Usage.OutputTokens)
	require.Equal(t, "kiro-estimated", result.BillingTier)

	out := rec.Body.String()
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, "event: content_block_start")
	require.Contains(t, out, "text_delta")
	require.Contains(t, out, "hi there")
	require.Contains(t, out, "event: content_block_stop")
	require.Contains(t, out, "event: message_delta")
	require.Contains(t, out, "event: message_stop")
}

func TestKiroGatewayService_Forward_Streaming_MidStreamReadErrorSendsSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"partial answer","inputTokens":8,"outputTokens":3}`))
	truncatedPrelude := []byte{0, 0, 0, 20}
	upstream := &kiroSequenceUpstream{bodies: [][]byte{append(frame, truncatedPrelude...)}}

	svc := NewKiroGatewayService(upstream, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 16,
		"stream":     true,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4", Stream: true}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, 1, upstream.calls, "committed stream must not be retried")
	require.True(t, IsResponseCommitted(c))

	out := rec.Body.String()
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, "partial answer")
	require.Contains(t, out, "event: error")
	require.Contains(t, out, `"type":"stream_read_error"`)
	require.Contains(t, out, "upstream stream disconnected: unexpected EOF")
	require.NotContains(t, out, "event: message_delta")
	require.NotContains(t, out, "event: message_stop")

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "stream_error", events[0].Kind)
	require.Equal(t, PlatformKiro, events[0].Platform)
	require.Equal(t, int64(99), events[0].AccountID)
	require.Contains(t, events[0].Message, "unexpected EOF")
}

func TestKiroGatewayService_Forward_Streaming_PreContentReadErrorTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := &kiroSequenceUpstream{bodies: [][]byte{{0, 0, 0, 20}}}
	svc := NewKiroGatewayService(upstream, nil, nil)
	body, _ := json.Marshal(map[string]any{
		"model":    "claude-sonnet-4",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   true,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4", Stream: true}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Empty(t, rec.Body.String(), "no SSE bytes may be written before failover")
	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "response_error", events[0].Kind)
	require.Equal(t, "unexpected_eof", events[0].Reason)
	require.Equal(t, "unexpected EOF", events[0].Message)
}

// kiroStatusUpstream returns a canned non-200 response with a fixed body,
// modeling the Kiro upstream rejecting a request (e.g. 400 INVALID_MODEL_ID).
// The vendored Kiro client reads the body into its error string, so both
// endpoints in the supported fallback list see the same rejection.
type kiroStatusUpstream struct {
	status int
	body   string
}

func (u *kiroStatusUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected Do call")
}

func (u *kiroStatusUpstream) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return &http.Response{
		StatusCode: u.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader([]byte(u.body))),
	}, nil
}

// Bug2: upstream returns 400 INVALID_MODEL_ID *before* any content is produced.
// The fix makes message_start lazy, so enc.started stays false and Forward must
// return a typed *KiroInvalidModelError instead of closing out a clean empty
// 200 SSE stream (the old "200 lie").
func TestKiroGatewayService_Forward_Streaming_InvalidModel_NoEmpty200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := &kiroStatusUpstream{
		status: http.StatusBadRequest,
		body:   `{"reason":"INVALID_MODEL_ID","message":"model not found"}`,
	}
	svc := NewKiroGatewayService(upstream, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"model":      "claude-haiku-4.5",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 16,
		"stream":     true,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-haiku-4.5", Stream: true}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.Error(t, err)
	require.Nil(t, result)

	// Error must be the typed invalid-model error carrying status 400 + model.
	var invalidModelErr *KiroInvalidModelError
	require.ErrorAs(t, err, &invalidModelErr)
	require.Equal(t, 400, invalidModelErr.StatusCode)
	require.Equal(t, "claude-haiku-4.5", invalidModelErr.Model)
	require.Contains(t, invalidModelErr.ClientMessage(), "not supported by Kiro")

	// No SSE was written: the old bug emitted message_start eagerly and returned
	// a clean empty 200 stream. The fix must write nothing to the client.
	require.Empty(t, rec.Body.String(), "no SSE bytes may be written before a pre-content 400")
	require.NotContains(t, rec.Body.String(), "message_start")
}

func TestKiroGatewayService_Forward_EventStreamThrottlingTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := &kiroFakeUpstream{
		body: buildKiroEventStreamException("ThrottlingException", []byte(`{"message":"slow down"}`)),
	}
	svc := NewKiroGatewayService(upstream, nil, nil)
	body, _ := json.Marshal(map[string]any{
		"model":    "claude-sonnet-4",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   false,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4", Stream: false}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
}

func TestKiroGatewayService_Forward_EmptyEventStreamExceptionPreservesFailoverClass(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := &kiroFakeUpstream{
		body: buildKiroEventStreamException("ThrottlingException", nil),
	}
	svc := NewKiroGatewayService(upstream, nil, nil)
	body, _ := json.Marshal(map[string]any{
		"model":    "claude-sonnet-4",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   false,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4", Stream: false}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusTooManyRequests, failoverErr.StatusCode)
}

func TestClassifyKiroForwardError_EventStreamValidationDoesNotFailover(t *testing.T) {
	_, err := classifyKiroForwardError(
		fmt.Errorf(`kiro event stream error: ValidationException: {"message":"invalid tool schema"}`),
		"claude-sonnet-4",
	)
	var invalidRequestErr *KiroInvalidRequestError
	require.ErrorAs(t, err, &invalidRequestErr)
	require.Equal(t, http.StatusBadRequest, invalidRequestErr.StatusCode)
	require.Equal(t, "invalid tool schema", invalidRequestErr.ClientMessage())

	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
}

func TestClassifyKiroForwardError_EventStreamInputTooLongDoesNotFailover(t *testing.T) {
	_, err := classifyKiroForwardError(
		fmt.Errorf(`kiro event stream error: CONTENT_LENGTH_EXCEEDS_THRESHOLD: {"message":"Your input exceeds the context window of this model. Please adjust your input and try again."}`),
		"claude-sonnet-4-6",
	)
	var invalidRequestErr *KiroInvalidRequestError
	require.ErrorAs(t, err, &invalidRequestErr)
	require.Equal(t, http.StatusBadRequest, invalidRequestErr.StatusCode)
	require.Contains(t, invalidRequestErr.ClientMessage(), "input exceeds the context window")

	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
}

func TestClassifyKiroForwardError_EventStreamProviderExceptionWinsOverInputTooLongText(t *testing.T) {
	_, err := classifyKiroForwardError(
		fmt.Errorf(`kiro event stream error: InternalServerException: {"message":"upstream failed while checking whether input exceeds the context window"}`),
		"claude-sonnet-4-6",
	)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)

	var invalidRequestErr *KiroInvalidRequestError
	require.NotErrorAs(t, err, &invalidRequestErr)
}

func TestKiroGatewayService_Forward_NonStreaming_InvalidModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := &kiroStatusUpstream{
		status: http.StatusBadRequest,
		body:   `{"reason":"INVALID_MODEL_ID"}`,
	}
	svc := NewKiroGatewayService(upstream, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"model":    "claude-haiku-4.5",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   false,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-haiku-4.5", Stream: false}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	var invalidModelErr *KiroInvalidModelError
	require.ErrorAs(t, err, &invalidModelErr)
	require.Equal(t, "claude-haiku-4.5", invalidModelErr.Model)
}

func TestClassifyKiroForwardError(t *testing.T) {
	// 400 + INVALID_MODEL_ID → typed error.
	_, err := classifyKiroForwardError(
		fmt.Errorf("HTTP 400 from CodeWhisperer: {\"reason\":\"INVALID_MODEL_ID\"}"),
		"claude-haiku-4.5",
	)
	var invalidModelErr *KiroInvalidModelError
	require.ErrorAs(t, err, &invalidModelErr)
	require.Equal(t, "claude-haiku-4.5", invalidModelErr.Model)

	// 400 without the INVALID_MODEL_ID marker → failover error, NOT typed invalid-model.
	_, other := classifyKiroForwardError(
		fmt.Errorf("HTTP 400 from CodeWhisperer: {\"reason\":\"THROTTLED\"}"),
		"claude-haiku-4.5",
	)
	require.Error(t, other)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, other, &failoverErr)
	require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)

	_, validation := classifyKiroForwardError(
		fmt.Errorf("HTTP 400 from CodeWhisperer: {\"__type\":\"ValidationException\",\"message\":\"invalid tool schema\"}"),
		"claude-sonnet-4",
	)
	var invalidRequestErr *KiroInvalidRequestError
	require.ErrorAs(t, validation, &invalidRequestErr)
	require.Equal(t, "invalid tool schema", invalidRequestErr.ClientMessage())
	require.NotErrorAs(t, other, &invalidModelErr)

	_, inputTooLong := classifyKiroForwardError(
		fmt.Errorf("HTTP 400 from CodeWhisperer: {\"reason\":\"CONTENT_LENGTH_EXCEEDS_THRESHOLD\",\"message\":\"Input is too long.\"}"),
		"claude-sonnet-4-6",
	)
	require.ErrorAs(t, inputTooLong, &invalidRequestErr)
	require.Equal(t, http.StatusBadRequest, invalidRequestErr.StatusCode)
	require.Equal(t, "Input is too long.", invalidRequestErr.ClientMessage())
	require.NotErrorAs(t, inputTooLong, &failoverErr)

	// 500 with the marker substring → still not classified as invalid-model.
	_, notFourHundred := classifyKiroForwardError(
		fmt.Errorf("HTTP 500 from CodeWhisperer: INVALID_MODEL_ID"),
		"claude-haiku-4.5",
	)
	require.NotErrorAs(t, notFourHundred, &invalidModelErr)
	require.ErrorAs(t, notFourHundred, &failoverErr)
	require.Equal(t, http.StatusInternalServerError, failoverErr.StatusCode)

	_, unauthorized := classifyKiroForwardError(
		fmt.Errorf("HTTP 401 from CodeWhisperer: Invalid bearer token"),
		"claude-sonnet-4",
	)
	require.ErrorAs(t, unauthorized, &failoverErr)
	require.Equal(t, http.StatusUnauthorized, failoverErr.StatusCode)
	require.Equal(t, "Invalid bearer token", string(failoverErr.ResponseBody))

	_, wrappedUnauthorized := classifyKiroForwardError(
		fmt.Errorf("resolve profileArn: HTTP 401 from management: Invalid bearer token"),
		"claude-sonnet-4",
	)
	require.ErrorAs(t, wrappedUnauthorized, &failoverErr)
	require.Equal(t, http.StatusUnauthorized, failoverErr.StatusCode)

	// nil passes through.
	_, nilErr := classifyKiroForwardError(nil, "m")
	require.NoError(t, nilErr)

	_, quota := classifyKiroForwardError(fmt.Errorf("quota exhausted on AmazonQ"), "claude-sonnet-4-5")
	var quotaErr *KiroEndpointQuotaExhaustedError
	require.ErrorAs(t, quota, &quotaErr)
	require.Equal(t, tkKiroEndpointQuotaExhaustedClient, quotaErr.ClientMessage())
}

func TestClassifyKiroForwardError_TransportFailureTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	account := newKiroAccountForTest()
	err := classifyAndRecordKiroForwardError(
		c,
		account,
		fmt.Errorf("GET https://q.us-east-1.amazonaws.com/generate?access_token=secret: dial tcp 10.0.0.1:443: connect: connection refused"),
		"claude-sonnet-4",
	)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.JSONEq(t, `{"error":{"type":"upstream_error","message":"Upstream request failed"}}`, string(failoverErr.ResponseBody))

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "request_error", events[0].Kind)
	require.Equal(t, "connection_refused", events[0].Reason)
	require.Contains(t, events[0].Message, "dial tcp 10.0.0.1:443: connect: connection refused")
	require.Contains(t, events[0].Message, "access_token=***")
	require.NotContains(t, events[0].Message, "access_token=secret")
	require.Equal(t, PlatformKiro, events[0].Platform)
	require.Equal(t, account.ID, events[0].AccountID)
}

func TestClassifyKiroForwardError_ContextCanceledDoesNotTriggerFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := classifyAndRecordKiroForwardError(c, newKiroAccountForTest(), context.Canceled, "claude-sonnet-4")
	var failoverErr *UpstreamFailoverError
	require.Error(t, err)
	require.NotErrorAs(t, err, &failoverErr)
	require.ErrorIs(t, err, context.Canceled)
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded, "client cancellation must not be recorded as a Kiro upstream failure")
}

func TestGatewayService_Forward_Kiro401TriggersRateLimitRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := &kiroStatusUpstream{
		status: http.StatusUnauthorized,
		body:   "Invalid bearer token",
	}
	expiresAt := time.Now().Add(2 * time.Hour)
	account := newKiroOAuth401Account(730, expiresAt)
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	rateLimit := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	rateLimit.SetOAuthRefreshAPI(NewOAuthRefreshAPI(repo, nil))
	rateLimit.SetKiroOAuthRefreshExecutor(&refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "new-at",
			"refresh_token": "new-rt",
			"expires_at":    expiresAt.Add(time.Hour).UTC().Format(time.RFC3339),
		},
	})

	svc := &GatewayService{
		kiroGateway:      NewKiroGatewayService(upstream, nil, nil),
		rateLimitService: rateLimit,
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "claude-sonnet-4",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   false,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4", Stream: false}

	result, err := svc.Forward(context.Background(), c, account, parsed)

	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusUnauthorized, failoverErr.StatusCode)
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Equal(t, 1, repo.setSchedulableCalls)
}
