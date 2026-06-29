//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

func TestKiroSSEEncoder_RedactsReasoningContentEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := &kiroSSEEncoder{
		w:       rec,
		flusher: rec,
		model:   "claude-sonnet-4-6",
		msgID:   "msg_test",
	}

	enc.writeThinkingDelta("The user asked who I am.")
	enc.writeThinkingDelta(" I should answer briefly.")
	enc.writeTextDelta("I am Claude.")
	enc.writeMessageDelta(12)
	enc.writeMessageStop()

	out := rec.Body.String()
	require.Contains(t, out, `"type":"redacted_thinking"`)
	require.Contains(t, out, kiroproto.RedactedThinkingData("The user asked who I am. I should answer briefly."))
	require.Contains(t, out, `"type":"text_delta"`)
	require.Contains(t, out, "I am Claude.")
	require.NotContains(t, out, "thinking_delta")
	require.NotContains(t, out, "The user asked who I am.")
}

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

func TestKiroSSEEncoder_InlineThinkingTagsRedacted(t *testing.T) {
	rec := httptest.NewRecorder()
	enc := &kiroSSEEncoder{
		w:       rec,
		flusher: rec,
		model:   "claude-sonnet-4-6",
		msgID:   "msg_test",
	}

	enc.writeThinkingDelta("from reasoning event")
	visible, inlineThinking := kiroproto.ExtractThinkingFromContent("<thinking>inline</thinking>Visible answer.")
	require.Equal(t, "inline", inlineThinking)
	enc.writeThinkingDelta(inlineThinking)
	enc.writeTextDelta(visible)
	enc.writeMessageDelta(8)
	enc.writeMessageStop()

	out := rec.Body.String()
	combined := "from reasoning eventinline"
	require.Contains(t, out, "redacted_thinking")
	require.Contains(t, out, kiroproto.RedactedThinkingData(combined))
	require.Contains(t, out, "Visible answer.")
	require.NotContains(t, out, "<thinking>")
	require.NotContains(t, out, "thinking_delta")
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
	require.Contains(t, out, "redacted_thinking")
	require.Contains(t, out, "final answer")
	require.NotContains(t, out, "thinking_delta")
	require.NotContains(t, out, "plan step one")
}

func TestKiroGatewayService_Forward_Streaming_RedactsSplitInlineThinkingTags(t *testing.T) {
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
	require.Contains(t, out, `"type":"redacted_thinking"`)
	require.Contains(t, out, "I am Claude.")
	require.NotContains(t, out, "<thinking>")
	require.NotContains(t, out, "</thinking>")
	require.NotContains(t, out, "The user asks who I am.")
	require.NotContains(t, out, "thinking_delta")
}
