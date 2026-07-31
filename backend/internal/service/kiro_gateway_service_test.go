//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
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
	bodies   [][]byte
	calls    int
	requests [][]byte
}

func (u *kiroSequenceUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected Do call")
}

func (u *kiroSequenceUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		u.requests = append(u.requests, body)
	}
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

func appendKiroTerminalStop(stream []byte, reason string) []byte {
	return append(stream, buildKiroEventStreamMessage("metadataEvent", []byte(fmt.Sprintf(`{"stopReason":%q}`, reason)))...)
}

func TestClassifyKiroPostOutputStreamError_OnlyMarksIncompleteEOF(t *testing.T) {
	require.True(t, IsKiroPostOutputStreamDisconnect(classifyKiroPostOutputStreamError("read", io.ErrUnexpectedEOF)))
	require.False(t, IsKiroPostOutputStreamDisconnect(classifyKiroPostOutputStreamError("read", context.Canceled)))
	require.False(t, IsKiroPostOutputStreamDisconnect(classifyKiroPostOutputStreamError("callback", errors.New("provider exception"))))
}

func TestMapKiroStopReason_PreservesTerminalOutcome(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		hasToolUse bool
		want       string
		wantErr    bool
	}{
		{name: "end turn", raw: "END_TURN", want: "end_turn"},
		{name: "end turn with tool", raw: "END_TURN", hasToolUse: true, want: "tool_use"},
		{name: "tool use", raw: "TOOL_USE", want: "tool_use"},
		{name: "max tokens", raw: "MAX_TOKENS", want: "max_tokens"},
		{name: "context window", raw: "MODEL_CONTEXT_WINDOW_EXCEEDED", want: "model_context_window_exceeded"},
		{name: "stop sequence without matched value fails closed", raw: "STOP_SEQUENCE", wantErr: true},
		{name: "filtered refusal text", raw: "CONTENT_FILTERED", want: "refusal"},
		{name: "guardrail refusal text", raw: "GUARDRAIL_INTERVENED", want: "refusal"},
		{name: "malformed model output fails closed", raw: "MALFORMED_MODEL_OUTPUT", wantErr: true},
		{name: "malformed tool use fails closed", raw: "MALFORMED_TOOL_USE", wantErr: true},
		{name: "unknown fails closed", raw: "MODEL_LIMIT", wantErr: true},
		{name: "missing fails closed", raw: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mapKiroStopReason(tt.raw, tt.hasToolUse)
			if tt.wantErr {
				require.ErrorIs(t, err, errKiroUnsupportedStopReason)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestKiroGatewayService_Forward_NonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"hello world","inputTokens":12,"outputTokens":5}`))
	frame = appendKiroTerminalStop(frame, "END_TURN")
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
	require.Equal(t, "end_turn", resp["stop_reason"])
}

func TestKiroGatewayService_Forward_NonStreaming_PreservesMaxTokensStopReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	frame := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"partial answer"}`))
	frame = appendKiroTerminalStop(frame, "MAX_TOKENS")
	svc := NewKiroGatewayService(&kiroFakeUpstream{body: frame}, nil, nil)

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(false), time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "max_tokens", resp["stop_reason"])
}

func TestKiroGatewayService_Forward_Streaming_PreservesMaxTokensStopReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	frame := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"partial answer"}`))
	frame = appendKiroTerminalStop(frame, "MAX_TOKENS")
	svc := NewKiroGatewayService(&kiroFakeUpstream{body: frame}, nil, nil)

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(true), time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), `"stop_reason":"max_tokens"`)
	require.NotContains(t, rec.Body.String(), `"stop_reason":"end_turn"`)
	require.Contains(t, rec.Body.String(), "event: message_stop")
}

func TestKiroGatewayService_Forward_Streaming_UnknownStopReasonFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	frame := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"partial answer"}`))
	frame = appendKiroTerminalStop(frame, "MODEL_LIMIT")
	svc := NewKiroGatewayService(&kiroFakeUpstream{body: frame}, nil, nil)

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(true), time.Now())
	require.ErrorIs(t, err, errKiroUnsupportedStopReason)
	require.Nil(t, result)
	require.Contains(t, rec.Body.String(), "event: error")
	require.Contains(t, rec.Body.String(), `"type":"unsupported_stop_reason"`)
	require.NotContains(t, rec.Body.String(), "event: message_delta")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
}

func TestKiroGatewayService_Forward_NonStreaming_UnknownStopReasonFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	frame := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"partial answer"}`))
	frame = appendKiroTerminalStop(frame, "MODEL_LIMIT")
	svc := NewKiroGatewayService(&kiroFakeUpstream{body: frame}, nil, nil)

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(false), time.Now())
	require.Error(t, err)
	require.Nil(t, result)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.Empty(t, rec.Body.String())
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
	require.Equal(t, "unexpected_eof", events[0].Reason)
	require.Contains(t, events[0].Message, "before terminal stop reason")
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

func newClaudeCodeKiroParsedRequestForTest(stream bool) *ParsedRequest {
	body, _ := json.Marshal(map[string]any{
		"model": "claude-sonnet-4-5",
		"system": strings.Join([]string{
			"You are Claude Code, Anthropic's official CLI for Claude.",
			"You are an interactive agent that helps users with software engineering tasks.",
			"# doing tasks",
			"# using your tools",
		}, "\n"),
		"messages":   []map[string]any{{"role": "user", "content": "implement and verify the change"}},
		"max_tokens": 1024,
		"stream":     stream,
	})
	return &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5", Stream: stream}
}

func newKiroToolRequestForTest(stream, claudeCode bool, toolNames ...string) *ParsedRequest {
	system := "You are a general API assistant."
	if claudeCode {
		system = strings.Join([]string{
			"You are Claude Code, Anthropic's official CLI for Claude.",
			"You are an interactive agent that helps users with software engineering tasks.",
			"# doing tasks",
			"# using your tools",
		}, "\n")
	}
	tools := make([]map[string]any, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, map[string]any{
			"name":         name,
			"description":  "test tool",
			"input_schema": map[string]any{"type": "object"},
		})
	}
	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-5",
		"system":     system,
		"messages":   []map[string]any{{"role": "user", "content": "use the requested tool"}},
		"max_tokens": 1024,
		"stream":     stream,
		"tools":      tools,
	})
	return &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5", Stream: stream}
}

func kiroTextStopStream(text, reason string) []byte {
	stream := buildKiroEventStreamMessage("assistantResponseEvent", []byte(fmt.Sprintf(`{"content":%q}`, text)))
	return appendKiroTerminalStop(stream, reason)
}

func kiroCompletionSignalStream(status, message string) []byte {
	return kiroToolUseStream("toolu_completion", "sub2apiClaudeCodeCompletion", map[string]any{
		"status":  status,
		"message": message,
	})
}

func kiroToolUseStream(id, name string, input map[string]any) []byte {
	stream := kiroToolUseEvent(id, name, input)
	return appendKiroTerminalStop(stream, "TOOL_USE")
}

func kiroToolUseEvent(id, name string, input map[string]any) []byte {
	payload, _ := json.Marshal(map[string]any{
		"toolUseId": id,
		"name":      name,
		"input":     input,
		"stop":      true,
	})
	return buildKiroEventStreamMessage("toolUseEvent", payload)
}

func TestUS041_KiroGatewayService_ClaudeCodeEndTurnContinuesUntilExplicitCompletion(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroTextStopStream("Implemented and verified the fix. ", "END_TURN"),
				kiroCompletionSignalStream("complete", "recap: Implemented and verified the fix."),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 2, upstream.calls, "END_TURN without explicit completion must continue inside the same client request")
			require.Len(t, upstream.requests, 2)
			require.Contains(t, string(upstream.requests[1]), "preceding assistant response ended without the required transport completion signal")
			require.Contains(t, string(upstream.requests[1]), "Implemented and verified the fix.")

			out := rec.Body.String()
			require.Equal(t, 1, strings.Count(out, "Implemented and verified the fix."))
			require.NotContains(t, out, "recap:", "hidden complete message is transport-only once text is visible")
			require.Contains(t, out, `"stop_reason":"end_turn"`)
			require.NotContains(t, out, "sub2apiClaudeCodeCompletion", "private completion tool must not leak to Claude Code")
		})
	}
}

func TestUS041_KiroGatewayService_EmptyCompletionSignalDoesNotFinish(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroCompletionSignalStream("complete", " "),
				kiroCompletionSignalStream("complete", "Implemented and verified the fix."),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 2, upstream.calls, "an empty private signal must not become an empty successful response")
			out := rec.Body.String()
			require.Contains(t, out, "Implemented and verified the fix.")
			require.Contains(t, out, `"stop_reason":"end_turn"`)
		})
	}
}

func TestUS041_KiroGatewayService_EmptyCompletionMessageWithAssistantTextDoesNotFinish(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"Implemented and verified the fix."}`))
			body = append(body, kiroToolUseEvent("toolu_completion", "sub2apiClaudeCodeCompletion", map[string]any{
				"status": "complete", "message": "",
			})...)
			body = appendKiroTerminalStop(body, "TOOL_USE")
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				body,
				kiroCompletionSignalStream("complete", "recap: completion confirmed."),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 2, upstream.calls, "assistant text must not substitute for an empty private completion message")
			out := rec.Body.String()
			require.Contains(t, out, "Implemented and verified the fix.")
			require.NotContains(t, out, "recap:", "later complete message must not become a second answer")
			require.Contains(t, out, `"stop_reason":"end_turn"`)
		})
	}
}

func TestUS041_KiroGatewayService_ClaudeCodeCompletionLoopIsBounded(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroTextStopStream("still only a progress update\n", "END_TURN"),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, maxClaudeCodeCompletionTurns, upstream.calls)
			if stream {
				require.Contains(t, rec.Body.String(), `"type":"completion_exhausted"`)
				require.Contains(t, rec.Body.String(), "event: error")
				require.NotContains(t, rec.Body.String(), "event: message_stop")
				return
			}
			require.Empty(t, rec.Body.String())
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
		})
	}
}

func TestUS041_KiroGatewayService_CompletionSignalMessageIsPreservedAfterText(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"progress before final signal"}`))
			body = append(body, kiroToolUseEvent("toolu_completion", "sub2apiClaudeCodeCompletion", map[string]any{
				"status": "complete", "message": "complete final answer",
			})...)
			body = appendKiroTerminalStop(body, "TOOL_USE")
			upstream := &kiroSequenceUpstream{bodies: [][]byte{body}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			out := rec.Body.String()
			require.Contains(t, out, "progress before final signal")
			require.Contains(t, out, "complete final answer")
			require.Equal(t, 1, strings.Count(out, "progress before final signal"))
			require.NotContains(t, out, "sub2apiClaudeCodeCompletion")
		})
	}
}

func TestUS041_KiroGatewayService_CompletionSignalDoesNotRepeatVisibleFinalText(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"Workspace is clean。PR #1501 is ready.\nChecks passed."}`))
			body = append(body, kiroToolUseEvent("toolu_completion", "sub2apiClaudeCodeCompletion", map[string]any{
				"status": "complete", "message": "PR #1501 is ready.\nChecks passed.",
			})...)
			body = appendKiroTerminalStop(body, "TOOL_USE")
			upstream := &kiroSequenceUpstream{bodies: [][]byte{body}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, upstream.calls)
			out := rec.Body.String()
			require.Equal(t, 1, strings.Count(out, "PR #1501 is ready."))
			require.Equal(t, 1, strings.Count(out, "Checks passed."))
			require.Contains(t, out, "Workspace is clean。")
			require.Contains(t, out, `"stop_reason":"end_turn"`)
			require.NotContains(t, out, "sub2apiClaudeCodeCompletion")
		})
	}
}

func TestUS041_KiroGatewayService_ContinuationCompletionDoesNotRepeatPriorFinalText(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroTextStopStream("Workspace is clean。PR #1501 is ready.\nChecks passed.", "END_TURN"),
				kiroCompletionSignalStream("complete", "PR #1501 is ready.\nChecks passed."),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 2, upstream.calls)
			out := rec.Body.String()
			require.Equal(t, 1, strings.Count(out, "PR #1501 is ready."))
			require.Equal(t, 1, strings.Count(out, "Checks passed."))
			require.Contains(t, out, "Workspace is clean。")
			require.Contains(t, out, `"stop_reason":"end_turn"`)
			require.NotContains(t, out, "sub2apiClaudeCodeCompletion")
		})
	}
}

func TestUS041_KiroGatewayService_HiddenCompletionSuppressesSemanticRecapAndToolOutputSummary(t *testing.T) {
	const visibleAnswer = "盯盘已运行，当前无需进一步动作。"
	const hiddenRecap = "recap: 已完成三窗检查。user 1 请求 57，计费 2.2547；另有 2 条客户端 400。"
	const hiddenMessage = "三窗结果正常，定时任务下次在 :43 自动运行。"

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			hiddenTurn := buildKiroEventStreamMessage("assistantResponseEvent", []byte(fmt.Sprintf(`{"content":%q}`, hiddenRecap)))
			hiddenTurn = append(hiddenTurn, kiroToolUseEvent("toolu_completion", "sub2apiClaudeCodeCompletion", map[string]any{
				"status": "complete", "message": hiddenMessage,
			})...)
			hiddenTurn = appendKiroTerminalStop(hiddenTurn, "TOOL_USE")
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroTextStopStream(visibleAnswer, "END_TURN"),
				hiddenTurn,
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 2, upstream.calls)
			out := rec.Body.String()
			require.Equal(t, 1, strings.Count(out, visibleAnswer))
			require.NotContains(t, out, "recap:")
			require.NotContains(t, out, "user 1 请求 57")
			require.NotContains(t, out, hiddenMessage)
			require.Contains(t, out, `"stop_reason":"end_turn"`)
		})
	}
}

func TestUS041_KiroGatewayService_HiddenBlockedMessageRemainsVisible(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroTextStopStream("已完成安全检查。", "END_TURN"),
				kiroCompletionSignalStream("blocked", "需要你确认是否部署到生产。"),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			out := rec.Body.String()
			require.Contains(t, out, "已完成安全检查。")
			require.Contains(t, out, "需要你确认是否部署到生产。")
			require.Contains(t, out, `"stop_reason":"end_turn"`)
		})
	}
}

func TestUS041_KiroGatewayService_HiddenMaxTokensTextRemainsVisible(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroTextStopStream("已开始处理。", "END_TURN"),
				kiroTextStopStream("达到本轮输出上限前的有效结果。", "MAX_TOKENS"),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			out := rec.Body.String()
			require.Contains(t, out, "已开始处理。")
			require.Contains(t, out, "达到本轮输出上限前的有效结果。")
			require.Contains(t, out, `"stop_reason":"max_tokens"`)
		})
	}
}

func TestUS041_KiroGatewayService_ContinuationTextDoesNotRepeatAcrossTurns(t *testing.T) {
	const summary = "清理完成，全程可回滚。\n\n## 做了什么\n\nmain 从 062c77b81 fast-forward 到 57f04ad4d。"
	const hiddenRecap = "recap: 仓库清理已经完成；git output 显示 main 已更新到 57f04ad4d。"
	const hiddenMessage = "最终确认：仓库状态正常。"
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroTextStopStream(summary, "END_TURN"),
				kiroTextStopStream(hiddenRecap, "END_TURN"),
				kiroCompletionSignalStream("complete", hiddenMessage),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 3, upstream.calls)
			require.Equal(t, 1, strings.Count(rec.Body.String(), "清理完成"))
			require.Equal(t, 1, strings.Count(rec.Body.String(), "57f04ad4d"))
			require.NotContains(t, rec.Body.String(), "recap:")
			require.NotContains(t, rec.Body.String(), "git output")
			require.NotContains(t, rec.Body.String(), hiddenMessage)
		})
	}
}

func TestContinuationTextDeltaKeepsOnlyNewToolContext(t *testing.T) {
	visible := "清理完成，全程可回滚。\n\n## 做了什么\n\nmain 从 062c77b81 fast-forward 到 57f04ad4d。"
	continuation := "调用 Read 前补充新上下文。\n\n清理完成，全程可回滚。\n\n## 做了什么\n\nmain 从 062c77b81 fast-forward 到 57f04ad4d。"
	require.Equal(t, "\n\n调用 Read 前补充新上下文。", continuationTextDelta(visible, continuation))
}

func TestContinuationTextDeltaKeepsParagraphBoundary(t *testing.T) {
	visible := "A completed paragraph with enough text."
	continuation := visible + "\n\nNew details from the continuation."
	require.Equal(t, "\n\nNew details from the continuation.", continuationTextDelta(visible, continuation))
}

func TestCompletionSignalTextDeltaPreservesNegativeNumber(t *testing.T) {
	signal := &kiroproto.ClaudeCodeCompletionSignal{Status: "complete", Message: "-10"}
	require.Equal(t, "\n\n-10", completionSignalTextDelta("Earlier result: 10", signal))

	markdownSignal := &kiroproto.ClaudeCodeCompletionSignal{Status: "complete", Message: "- Done and verified."}
	require.Empty(t, completionSignalTextDelta("Done and verified.", markdownSignal))
}

func TestUS041_KiroGatewayService_ContinuationToolPreservesTextBeforeTool(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			toolTurn := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"new context before Read"}`))
			toolTurn = append(toolTurn, kiroToolUseEvent("toolu_read", "Read", map[string]any{"file_path": "a.go"})...)
			toolTurn = appendKiroTerminalStop(toolTurn, "TOOL_USE")
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroTextStopStream("initial progress", "END_TURN"),
				toolTurn,
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newKiroToolRequestForTest(stream, true, "Read"), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			out := rec.Body.String()
			textIndex := strings.Index(out, "new context before Read")
			toolIndex := strings.Index(out, `"name":"Read"`)
			require.NotEqual(t, -1, textIndex)
			require.NotEqual(t, -1, toolIndex)
			require.Less(t, textIndex, toolIndex)
			require.Contains(t, out, `"stop_reason":"tool_use"`)
		})
	}
}

func TestUS041_KiroGatewayService_CompletionBillingCountsPrivateToolOnce(t *testing.T) {
	const message = "Implemented and verified the fix."
	privateTool := kiroproto.KiroToolUse{
		ToolUseID: "toolu_completion",
		Name:      "sub2apiClaudeCodeCompletion",
		Input: map[string]any{
			"status":  "complete",
			"message": message,
		},
	}
	wantOutputTokens := kiroproto.EstimateOutputTokens("", "", []kiroproto.KiroToolUse{privateTool})

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroCompletionSignalStream("complete", message),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, wantOutputTokens, result.Usage.OutputTokens)
			require.Contains(t, rec.Body.String(), message)
		})
	}
}

func TestUS041_KiroGatewayService_ContinuationStreamingReportsFinalInputUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	upstream := &kiroSequenceUpstream{bodies: [][]byte{
		kiroTextStopStream("initial progress", "END_TURN"),
		kiroCompletionSignalStream("complete", "Implemented and verified the fix."),
	}}

	svc := NewKiroGatewayService(upstream, nil, nil)
	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
		newClaudeCodeKiroParsedRequestForTest(true), time.Now())

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, upstream.calls)
	require.Contains(t, rec.Body.String(), fmt.Sprintf(
		`"usage":{"input_tokens":%d,"output_tokens":%d}`,
		result.Usage.InputTokens,
		result.Usage.OutputTokens,
	))
}

func TestUS041_KiroGatewayService_NonClaudeCodeCompletionNamedToolIsPreserved(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroCompletionSignalStream("complete", "client-owned tool result"),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newKiroToolRequestForTest(stream, false, "sub2apiClaudeCodeCompletion"), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, upstream.calls)
			out := rec.Body.String()
			require.Contains(t, out, `"name":"sub2apiClaudeCodeCompletion"`)
			require.Contains(t, out, `"stop_reason":"tool_use"`)
		})
	}
}

func TestUS041_KiroGatewayService_ClaudeCodeOrdinaryToolUseReturnsImmediately(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			upstream := &kiroSequenceUpstream{bodies: [][]byte{
				kiroToolUseStream("toolu_read", "Read", map[string]any{"file_path": "a.go"}),
			}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newKiroToolRequestForTest(stream, true, "Read"), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, upstream.calls, "ordinary Claude Code tools must return control to the client")
			out := rec.Body.String()
			require.Contains(t, out, `"name":"Read"`)
			require.Contains(t, out, `"stop_reason":"tool_use"`)
		})
	}
}

func TestUS041_KiroGatewayService_ClaudeCodeOrdinaryToolWinsOverCompletionSignal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			mixed := kiroToolUseEvent("toolu_completion", "sub2apiClaudeCodeCompletion", map[string]any{
				"status": "complete", "message": "premature completion",
			})
			mixed = append(mixed, kiroToolUseEvent("toolu_read", "Read", map[string]any{"file_path": "a.go"})...)
			mixed = appendKiroTerminalStop(mixed, "TOOL_USE")
			upstream := &kiroSequenceUpstream{bodies: [][]byte{mixed}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newKiroToolRequestForTest(stream, true, "Read"), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, upstream.calls)
			out := rec.Body.String()
			require.Contains(t, out, `"name":"Read"`)
			require.NotContains(t, out, "premature completion")
			require.NotContains(t, out, `"name":"sub2apiClaudeCodeCompletion"`)
			require.Contains(t, out, `"stop_reason":"tool_use"`)
		})
	}
}

func TestUS041_KiroGatewayService_MaxTokensOverridesCompletionSignal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := kiroToolUseEvent("toolu_completion", "sub2apiClaudeCodeCompletion", map[string]any{
				"status": "complete", "message": "not authoritative for max tokens",
			})
			body = appendKiroTerminalStop(body, "MAX_TOKENS")
			upstream := &kiroSequenceUpstream{bodies: [][]byte{body}}

			svc := NewKiroGatewayService(upstream, nil, nil)
			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newClaudeCodeKiroParsedRequestForTest(stream), time.Now())

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, upstream.calls)
			out := rec.Body.String()
			require.NotContains(t, out, "not authoritative for max tokens")
			require.Contains(t, out, `"stop_reason":"max_tokens"`)
		})
	}
}

func requireKiroContentFilteredError(t *testing.T, c *gin.Context, rec *httptest.ResponseRecorder, result *ForwardResult, err error) {
	t.Helper()
	require.Nil(t, result)
	var contentFilteredErr *KiroContentFilteredError
	require.ErrorAs(t, err, &contentFilteredErr)
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Empty(t, rec.Body.String())

	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded, "client-owned content filtering must not create an upstream error event")
	_, recorded = c.Get(OpsUpstreamStatusCodeKey)
	require.False(t, recorded, "client-owned content filtering must not set upstream status context")
	_, recorded = c.Get(OpsUpstreamErrorMessageKey)
	require.False(t, recorded, "client-owned content filtering must not set upstream error context")
	require.True(t, HasOpsClientContentFiltered(c))
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

func TestKiroGatewayService_Forward_GuardrailIntervenedIsNotFailover(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			body := appendKiroTerminalStop(nil, "GUARDRAIL_INTERVENED")
			svc := NewKiroGatewayService(&kiroFakeUpstream{body: body}, nil, nil)

			result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
				newKiroParsedRequestForTest(stream), time.Now())

			requireKiroContentFilteredError(t, c, rec, result, err)
		})
	}
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
	require.Contains(t, rec.Body.String(), `"stop_reason":"refusal"`)
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded)
	require.False(t, HasOpsClientContentFiltered(c))
}

func TestKiroGatewayService_Forward_NonStreaming_ReadFailureRetriesWithoutPartialOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	partial := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"discard me"}`))
	partial = append(partial, []byte{0, 0, 0, 20}...)
	recovered := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"recovered"}`))
	recovered = appendKiroTerminalStop(recovered, "END_TURN")
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
	frame = appendKiroTerminalStop(frame, "END_TURN")
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
	require.True(t, IsKiroPostOutputStreamDisconnect(err))
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
	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.Equal(t, "upstream_error", streamErr.ErrType)
	require.Equal(t, http.StatusBadGateway, streamErr.IntendedStatus)
	require.Contains(t, streamErr.Message, "unexpected EOF")
}

func TestKiroGatewayService_Forward_Streaming_FrameAlignedEOFWithoutStopReasonSendsSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	partial := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"partial answer"}`))
	upstream := &kiroSequenceUpstream{bodies: [][]byte{partial}}
	svc := NewKiroGatewayService(upstream, nil, nil)
	body, _ := json.Marshal(map[string]any{
		"model":    "claude-opus-4-8",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   true,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-8", Stream: true}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.Nil(t, result)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.True(t, IsKiroPostOutputStreamDisconnect(err))
	require.Equal(t, 1, upstream.calls, "post-output failures must never replay the current stream")
	require.Contains(t, rec.Body.String(), "event: error")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
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
	require.False(t, IsKiroPostOutputStreamDisconnect(err), "pre-output EOF must stay on the existing safe failover path")
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
