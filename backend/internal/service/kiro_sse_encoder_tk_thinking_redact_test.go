//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
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
	enc.writeMessageDelta(0, 4, 0, 0, "end_turn")
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
	body = appendKiroTerminalStop(body, "END_TURN")
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
	require.Contains(t, out, `"type":"thinking_delta"`)
	require.Contains(t, out, `"thinking":"plan step one"`)
	require.NotContains(t, out, `"text":"plan step one"`)
	require.Less(t, strings.Index(out, `"type":"thinking_delta"`), strings.Index(out, `"type":"text_delta"`))
	require.Contains(t, out, `"type":"signature_delta"`)
	require.Contains(t, out, `"signature":"sig_`)
}

func TestKiroGatewayService_Forward_Streaming_StashesReasoningSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	reasoningFrame := buildKiroEventStreamMessage("reasoningContentEvent",
		[]byte(`{"text":"plan step one","signature":"UPSTREAM_SIG_GATEWAY"}`))
	textFrame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"final answer"}`))
	body := append(reasoningFrame, textFrame...)
	body = appendKiroTerminalStop(body, "END_TURN")
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
	require.NotContains(t, out, "UPSTREAM_SIG_GATEWAY")
	require.Contains(t, out, `"thinking":"plan step one"`)
	require.Contains(t, out, `"signature":"sig_`)
	require.Contains(t, out, "final answer")

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "plan step one")
	require.Contains(t, blocks[0], "UPSTREAM_SIG_GATEWAY")
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
	body = appendKiroTerminalStop(body, "END_TURN")
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
	require.NotContains(t, out, "<thinking>")
	require.NotContains(t, out, "</thinking>")
	require.NotContains(t, out, "The user asks who I am.")
	require.NotContains(t, out, "thinking_delta")
}

func TestKiroGatewayService_Forward_Streaming_StashesTaglessThinkingWithSignatureLiveOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := []byte{}
	for _, content := range []string{
		"17 × 23:\n\n17 × 20 = 340\n17 × 3 = 51\n340 + 51 = 391\n\n",
		"391",
	} {
		body = append(body, buildKiroEventStreamMessage("assistantResponseEvent",
			[]byte(`{"content":`+strconv.Quote(content)+`}`))...)
	}
	reasoningFrame := buildKiroEventStreamMessage("reasoningContentEvent",
		[]byte(`{"text":"...","signature":"TAGLESS_UPSTREAM_SIG"}`))
	body = append(body, reasoningFrame...)
	body = appendKiroTerminalStop(body, "END_TURN")
	upstream := &kiroFakeUpstream{body: body}

	svc := NewKiroGatewayService(upstream, nil, nil)
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"messages":   []map[string]any{{"role": "user", "content": "17*23"}},
		"max_tokens": 32,
		"stream":     true,
		"thinking":   map[string]any{"type": "adaptive", "display": "omitted"},
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-sonnet-4-6", Stream: true}

	_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)

	out := rec.Body.String()
	require.NotContains(t, out, "TAGLESS_UPSTREAM_SIG")
	require.NotContains(t, out, "thinking_delta")
	require.Contains(t, out, "391")
	require.Contains(t, out, "17 × 20 = 340")

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "17 × 20 = 340")
	require.Contains(t, blocks[0], "TAGLESS_UPSTREAM_SIG")
}

func TestKiroGatewayService_Forward_NonStreaming_StashesTaglessThinkingWithSignatureLiveOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := []byte{}
	for _, content := range []string{
		"17 × 23:\n\n17 × 20 = 340\n17 × 3 = 51\n340 + 51 = 391\n\n",
		"391",
	} {
		body = append(body, buildKiroEventStreamMessage("assistantResponseEvent",
			[]byte(`{"content":`+strconv.Quote(content)+`}`))...)
	}
	reasoningFrame := buildKiroEventStreamMessage("reasoningContentEvent",
		[]byte(`{"text":"...","signature":"TAGLESS_NS_SIG"}`))
	body = append(body, reasoningFrame...)
	body = appendKiroTerminalStop(body, "END_TURN")
	upstream := &kiroFakeUpstream{body: body}

	svc := NewKiroGatewayService(upstream, nil, nil)
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"messages":   []map[string]any{{"role": "user", "content": "17*23"}},
		"max_tokens": 32,
		"stream":     false,
		"thinking":   map[string]any{"type": "adaptive", "display": "omitted"},
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-sonnet-4-6", Stream: false}

	_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	content, ok := resp["content"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, content)
	respBody, _ := json.Marshal(resp)
	require.NotContains(t, string(respBody), "TAGLESS_NS_SIG")
	require.NotContains(t, string(respBody), `"type":"thinking"`)

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "17 × 20 = 340")
	require.Contains(t, blocks[0], "TAGLESS_NS_SIG")
}

func TestKiroGatewayService_Forward_NonStreaming_EmitsThinkingWithPlaceholderSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	body := buildKiroEventStreamMessage("reasoningContentEvent",
		[]byte(`{"text":"plan step one","signature":"UPSTREAM_SIG_NS"}`))
	body = append(body, buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"final answer"}`))...)
	body = appendKiroTerminalStop(body, "END_TURN")

	svc := NewKiroGatewayService(&kiroFakeUpstream{body: body}, nil, nil)
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 32,
		"stream":     false,
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": 1000},
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-sonnet-4-6", Stream: false}

	_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)

	var resp struct {
		Content []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Content, 2)
	require.Equal(t, "thinking", resp.Content[0].Type)
	require.Equal(t, "plan step one", resp.Content[0].Thinking)
	require.True(t, strings.HasPrefix(resp.Content[0].Signature, "sig_"))
	require.NotContains(t, resp.Content[0].Signature, "UPSTREAM_SIG_NS")
	require.Equal(t, "text", resp.Content[1].Type)
	require.Equal(t, "final answer", resp.Content[1].Text)

	raw, ok := c.Get(kiroInternalThinkingGinKey)
	require.True(t, ok)
	blocks, ok := raw.([]string)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.Contains(t, blocks[0], "UPSTREAM_SIG_NS")
	require.Contains(t, blocks[0], "plan step one")
}

func TestKiroGatewayService_Forward_PreservesThinkingLiterals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const answer = "Explain `<thinking>` literally.\n```xml\n<thinking>example\n```\nKeep the tail <thin"
	for _, stream := range []bool{false, true} {
		for _, reasoning := range []string{"none", "native", "inline"} {
			t.Run(strconv.FormatBool(stream)+"/"+reasoning, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				var body []byte
				switch reasoning {
				case "native":
					body = buildKiroEventStreamMessage("reasoningContentEvent", []byte(`{"text":"private plan","signature":"PRIVATE_SIGNATURE"}`))
				case "inline":
					body = buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"<thinking>private plan</thinking>"}`))
				}
				// Byte-sized events exercise delimiter splits through the real gateway.
				for _, ch := range answer {
					payload, err := json.Marshal(map[string]string{"content": string(ch)})
					require.NoError(t, err)
					body = append(body, buildKiroEventStreamMessage("assistantResponseEvent", payload)...)
				}
				body = appendKiroTerminalStop(body, "END_TURN")
				svc := NewKiroGatewayService(&kiroFakeUpstream{body: body}, nil, nil)
				reqBody, err := json.Marshal(map[string]any{
					"model": "claude-sonnet-4-6", "messages": []map[string]any{{"role": "user", "content": "explain the literal tag"}},
					"max_tokens": 128, "stream": stream,
					"thinking": map[string]any{"type": "enabled", "budget_tokens": 1000},
				})
				require.NoError(t, err)
				parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-sonnet-4-6", Stream: stream}
				_, err = svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
				require.NoError(t, err)
				var text, thinking strings.Builder
				if stream {
					for _, line := range strings.Split(rec.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						var event struct {
							Delta struct{ Type, Text, Thinking string }
						}
						require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
						if event.Delta.Type == "text_delta" {
							text.WriteString(event.Delta.Text)
						}
						if event.Delta.Type == "thinking_delta" {
							thinking.WriteString(event.Delta.Thinking)
						}
					}
				} else {
					var response struct {
						Content []struct{ Type, Text, Thinking string }
					}
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
					for _, block := range response.Content {
						if block.Type == "text" {
							text.WriteString(block.Text)
						}
						if block.Type == "thinking" {
							thinking.WriteString(block.Thinking)
						}
					}
				}
				require.Equal(t, answer, text.String())
				if reasoning == "native" {
					require.Equal(t, "private plan", thinking.String())
				} else {
					require.Empty(t, thinking.String())
				}
				require.NotContains(t, rec.Body.String(), "PRIVATE_SIGNATURE")
				if reasoning == "inline" {
					require.NotContains(t, rec.Body.String(), "private plan")
				}
			})
		}
	}
}
