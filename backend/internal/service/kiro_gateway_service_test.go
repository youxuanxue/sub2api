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
	require.Equal(t, 12, result.Usage.InputTokens)
	require.Equal(t, 5, result.Usage.OutputTokens)
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
	require.Equal(t, 8, result.Usage.InputTokens)
	require.Equal(t, 3, result.Usage.OutputTokens)

	out := rec.Body.String()
	require.Contains(t, out, "event: message_start")
	require.Contains(t, out, "event: content_block_start")
	require.Contains(t, out, "text_delta")
	require.Contains(t, out, "hi there")
	require.Contains(t, out, "event: content_block_stop")
	require.Contains(t, out, "event: message_delta")
	require.Contains(t, out, "event: message_stop")
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
