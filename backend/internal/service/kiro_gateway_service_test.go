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

// ---- IsKiroEnabled ----

type kiroSettingRepoStub struct {
	value string
	err   error
}

func (s *kiroSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get")
}
func (s *kiroSettingRepoStub) GetValue(_ context.Context, _ string) (string, error) {
	return s.value, s.err
}
func (s *kiroSettingRepoStub) Set(context.Context, string, string) error { panic("unexpected Set") }
func (s *kiroSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	panic("unexpected GetMultiple")
}
func (s *kiroSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple")
}
func (s *kiroSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll")
}
func (s *kiroSettingRepoStub) Delete(context.Context, string) error { panic("unexpected Delete") }

func newKiroSettingService(value string, err error) *SettingService {
	return NewSettingService(&kiroSettingRepoStub{value: value, err: err}, nil)
}

func TestSettingService_IsKiroEnabled(t *testing.T) {
	ctx := context.Background()
	require.True(t, newKiroSettingService("true", nil).IsKiroEnabled(ctx))
	require.False(t, newKiroSettingService("false", nil).IsKiroEnabled(ctx))
	require.False(t, newKiroSettingService("", nil).IsKiroEnabled(ctx))                // unset
	require.False(t, newKiroSettingService("", ErrSettingNotFound).IsKiroEnabled(ctx)) // default closed
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

	headerBytes := headers.Bytes()
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

	svc := NewKiroGatewayService(upstream, nil, newKiroSettingService("true", nil))

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

func TestKiroGatewayService_Forward_Streaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"hi there","inputTokens":8,"outputTokens":3}`))
	upstream := &kiroFakeUpstream{body: frame}

	svc := NewKiroGatewayService(upstream, nil, newKiroSettingService("true", nil))

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

// kiroStatusUpstream returns a canned non-200 response with a fixed body,
// modeling the Kiro upstream rejecting a request (e.g. 400 INVALID_MODEL_ID).
// The vendored CallKiroAPIWithDoer reads the body into its error string, so all
// three endpoints in the fallback list see the same rejection.
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
	svc := NewKiroGatewayService(upstream, nil, newKiroSettingService("true", nil))

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

func TestKiroGatewayService_Forward_NonStreaming_InvalidModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := &kiroStatusUpstream{
		status: http.StatusBadRequest,
		body:   `{"reason":"INVALID_MODEL_ID"}`,
	}
	svc := NewKiroGatewayService(upstream, nil, newKiroSettingService("true", nil))

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
	err := classifyKiroForwardError(
		fmt.Errorf("HTTP 400 from CodeWhisperer: {\"reason\":\"INVALID_MODEL_ID\"}"),
		"claude-haiku-4.5",
	)
	var invalidModelErr *KiroInvalidModelError
	require.ErrorAs(t, err, &invalidModelErr)
	require.Equal(t, "claude-haiku-4.5", invalidModelErr.Model)

	// 400 without the INVALID_MODEL_ID marker → generic wrapped error, NOT typed.
	other := classifyKiroForwardError(
		fmt.Errorf("HTTP 400 from CodeWhisperer: {\"reason\":\"THROTTLED\"}"),
		"claude-haiku-4.5",
	)
	require.Error(t, other)
	require.NotErrorAs(t, other, &invalidModelErr)
	require.Contains(t, other.Error(), "kiro upstream call failed")

	// 500 with the marker substring → still not classified as invalid-model.
	notFourHundred := classifyKiroForwardError(
		fmt.Errorf("HTTP 500 from CodeWhisperer: INVALID_MODEL_ID"),
		"claude-haiku-4.5",
	)
	require.NotErrorAs(t, notFourHundred, &invalidModelErr)

	// nil passes through.
	require.NoError(t, classifyKiroForwardError(nil, "m"))
}

func TestKiroGatewayService_Forward_DisabledErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	upstream := &kiroFakeUpstream{body: nil}
	svc := NewKiroGatewayService(upstream, nil, newKiroSettingService("false", nil))

	parsed := &ParsedRequest{Body: NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4"}`)), Model: "claude-sonnet-4"}
	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	require.Contains(t, err.Error(), "kiro platform is disabled")
	require.False(t, upstream.gotRequest, "upstream must not be called when disabled")
}
