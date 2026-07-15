//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroSSEEncoder_OpusStyleTextOnly_NoRedactedBlock(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := &kiroSSEEncoder{
		w:       rec,
		flusher: rec,
		model:   "claude-opus-4-8",
		msgID:   "msg_test",
	}

	enc.writeTextDelta("I am Claude.")
	enc.writeMessageDelta(4)
	enc.writeMessageStop()

	out := rec.Body.String()
	require.NotContains(t, out, "redacted_thinking")
	require.NotContains(t, out, "thinking_delta")
	require.Contains(t, out, "I am Claude.")
}

func TestKiroGatewayService_Forward_Streaming_WithReasoningEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	reasoningFrame := buildKiroEventStreamMessage("reasoningContentEvent",
		[]byte(`{"text":"plan step one"}`))
	textFrame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"final answer"}`))
	body := append(reasoningFrame, textFrame...)
	upstream := &kiroFakeUpstream{body: body}

	svc := NewKiroGatewayService(upstream, nil, nil)
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 32,
		"stream":     true,
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": 1000},
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-sonnet-4-6", Stream: true}

	_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)

	out := rec.Body.String()
	require.NotContains(t, out, "redacted_thinking")
	require.Contains(t, out, "final answer")
	require.NotContains(t, out, "thinking_delta")
	require.NotContains(t, out, "plan step one")
}

func TestKiroGatewayService_Forward_Streaming_OmitsSplitInlineThinkingTags(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := []byte{}
	for _, content := range []string{
		"<thin",
		"king>\nThe user asks who I am.",
		" I must not expose this.</thin",
		"king>I am Claude.",
	} {
		body = append(body, buildKiroEventStreamMessage("assistantResponseEvent",
			[]byte(`{"content":`+strconv.Quote(content)+`}`))...)
	}
	upstream := &kiroFakeUpstream{body: body}

	svc := NewKiroGatewayService(upstream, nil, nil)
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"messages":   []map[string]any{{"role": "user", "content": "who are you"}},
		"max_tokens": 32,
		"stream":     true,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-sonnet-4-6", Stream: true}

	_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)

	out := rec.Body.String()
	require.NotContains(t, out, "redacted_thinking")
	require.Contains(t, out, "I am Claude.")
	require.NotContains(t, out, kiroInternalThinkingSSECommentPfx)
	require.NotContains(t, out, "<thinking>")
	require.NotContains(t, out, "</thinking>")
	require.NotContains(t, out, "The user asks who I am.")
	require.NotContains(t, out, "thinking_delta")
}

func TestKiroGatewayService_Forward_Streaming_MirrorHopEmitsInternalThinkingSideChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set(kiroInternalThinkingMirrorHopRequestHeader, "1")

	body := []byte{}
	for _, content := range []string{
		"<thinking>hidden plan</thinking>visible answer",
	} {
		body = append(body, buildKiroEventStreamMessage("assistantResponseEvent",
			[]byte(`{"content":`+strconv.Quote(content)+`}`))...)
	}
	upstream := &kiroFakeUpstream{body: body}

	svc := NewKiroGatewayService(upstream, nil, nil)
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 32,
		"stream":     true,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-sonnet-4-6", Stream: true}

	_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)

	out := rec.Body.String()
	require.Contains(t, out, kiroInternalThinkingSSECommentPfx)
	require.NotContains(t, out, "hidden plan")
}
