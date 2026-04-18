package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ForwardAsAnthropicDispatched routes Anthropic Messages requests through the
// NewAPI bridge for newapi accounts by converting Anthropic → Chat Completions,
// dispatching through the bridge, and converting the response back to Anthropic
// format. For non-bridge accounts, it delegates to ForwardAsAnthropic.
func (s *OpenAIGatewayService) ForwardAsAnthropicDispatched(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	promptCacheKey string,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	if !s.ShouldDispatchToNewAPIBridge(account, BridgeEndpointChatCompletions) {
		return s.ForwardAsAnthropic(ctx, c, account, body, promptCacheKey, defaultMappedModel)
	}

	startTime := time.Now()
	recordBridgeDispatch()

	// 1. Parse Anthropic request
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, fmt.Errorf("parse anthropic request: %w", err)
	}
	originalModel := anthropicReq.Model
	applyOpenAICompatModelNormalization(&anthropicReq)
	normalizedModel := anthropicReq.Model
	clientStream := anthropicReq.Stream

	// 2. Model mapping
	billingModel := resolveOpenAIForwardModel(account, normalizedModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)

	// 3. Convert Anthropic → Chat Completions body
	chatBody, err := anthropicToChatCompletionsBody(&anthropicReq, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("convert anthropic to chat completions: %w", err)
	}

	// 4. Prepare bridge channel input
	auth := bridgeAuthFromGin(c)
	in := newAPIBridgeChannelInput(account, auth.UserID, auth.GroupName)
	if strings.TrimSpace(in.APIKey) == "" {
		recordBridgeDispatchError()
		return nil, &NewAPIRelayError{Err: errBridgeMissingCredential("api_key")}
	}

	// 5. Capture bridge output into a buffer.
	// The bridge relay reads c.Request.URL.Path to determine the relay mode;
	// swap to /v1/chat/completions so the relay recognises the Chat Completions path.
	var buf bytes.Buffer
	captureWriter := newBridgeCaptureWriter(&buf)
	origWriter := c.Writer
	origPath := c.Request.URL.Path
	c.Writer = captureWriter
	c.Request.URL.Path = "/v1/chat/completions"

	var dispatchPanic any
	var out *bridge.DispatchOutcome
	var apiErr *newapitypes.NewAPIError

	func() {
		defer func() {
			c.Writer = origWriter
			c.Request.URL.Path = origPath
			if r := recover(); r != nil {
				dispatchPanic = r
				logger.L().Error("openai_gateway.bridge_anthropic_dispatch_panic",
					zap.Int64("account_id", account.ID),
					zap.String("panic", fmt.Sprintf("%v", r)),
					zap.String("stack", string(debug.Stack())),
				)
			}
		}()
		out, apiErr = bridge.DispatchChatCompletions(ctx, c, in, chatBody)
	}()

	if dispatchPanic != nil {
		writeAnthropicError(c, http.StatusBadGateway, "api_error", "Bridge dispatch panicked")
		return nil, fmt.Errorf("bridge dispatch panic: %v", dispatchPanic)
	}

	if apiErr != nil {
		recordBridgeDispatchError()
		logger.L().Info("openai_gateway.newapi_bridge_anthropic_dispatch",
			zap.String("endpoint", "messages_via_chat_completions"),
			zap.Int("channel_type", account.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.Int64("account_id", account.ID),
		)
		statusCode := apiErr.StatusCode
		if statusCode < 400 || statusCode > 599 {
			statusCode = http.StatusBadGateway
		}
		claudeErr := apiErr.ToClaudeError()
		errType := strings.TrimSpace(claudeErr.Type)
		if errType == "" {
			errType = "api_error"
		}
		message := strings.TrimSpace(claudeErr.Message)
		if message == "" {
			message = "Bridge dispatch failed"
		}
		writeAnthropicError(c, statusCode, errType, message)
		return nil, &NewAPIRelayError{Err: apiErr}
	}

	logger.L().Info("openai_gateway.newapi_bridge_anthropic_dispatch",
		zap.String("endpoint", "messages_via_chat_completions"),
		zap.Int("channel_type", account.ChannelType),
		zap.String("bridge_path", "newapi_adaptor"),
		zap.String("adaptor_relay_format", bridge.DescribeRelayFormat(out.AdaptorRelayFmt)),
		zap.Int("adaptor_api_type", out.AdaptorAPIType),
		zap.Int64("account_id", account.ID),
	)

	if captureWriter.statusCode >= 400 {
		writeAnthropicError(c, http.StatusBadGateway, "api_error", "Upstream returned error")
		return nil, fmt.Errorf("bridge upstream error (status %d)", captureWriter.statusCode)
	}

	bridgeUpstream := strings.TrimSpace(out.UpstreamModel)
	if bridgeUpstream == "" {
		bridgeUpstream = out.Model
	}

	// 6. Convert buffered Chat Completions response → Anthropic format
	var result *OpenAIForwardResult
	var handleErr error
	if clientStream {
		result, handleErr = convertBufferedChatCompletionsToAnthropicSSE(
			c, &buf, originalModel, billingModel, bridgeUpstream, startTime)
	} else {
		result, handleErr = convertBufferedChatCompletionsToAnthropicJSON(
			c, &buf, originalModel, billingModel, bridgeUpstream, startTime)
	}

	if handleErr == nil && result != nil {
		result.Usage = openAIUsageFromNewAPIDTO(out.Usage)
	}

	return result, handleErr
}

// ---------------------------------------------------------------------------
// Anthropic → Chat Completions request conversion
// ---------------------------------------------------------------------------

func anthropicToChatCompletionsBody(req *apicompat.AnthropicRequest, upstreamModel string) ([]byte, error) {
	var messages []tkBridgeChatMsg

	if len(req.System) > 0 {
		sysText, err := tkBridgeParseAnthropicSystem(req.System)
		if err != nil {
			return nil, err
		}
		if sysText != "" {
			messages = append(messages, tkBridgeChatMsg{
				Role:    "system",
				Content: json.RawMessage(tkBridgeMustMarshalStr(sysText)),
			})
		}
	}

	for _, m := range req.Messages {
		converted, err := tkBridgeConvertAnthropicMsg(m)
		if err != nil {
			return nil, err
		}
		messages = append(messages, converted...)
	}

	chatReq := map[string]any{
		"model":    upstreamModel,
		"messages": messages,
		"stream":   true,
	}
	if req.MaxTokens > 0 {
		chatReq["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		chatReq["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		chatReq["top_p"] = *req.TopP
	}
	if len(req.StopSeqs) > 0 {
		chatReq["stop"] = req.StopSeqs
	}
	if len(req.Tools) > 0 {
		chatReq["tools"] = tkBridgeConvertTools(req.Tools)
	}
	if len(req.ToolChoice) > 0 {
		if tc, err := tkBridgeConvertToolChoice(req.ToolChoice); err == nil {
			chatReq["tool_choice"] = tc
		}
	}

	return json.Marshal(chatReq)
}

type tkBridgeChatMsg struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []tkBridgeTC    `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type tkBridgeTC struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function tkBridgeFnCall `json:"function"`
}

type tkBridgeFnCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func tkBridgeParseAnthropicSystem(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", err
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func tkBridgeConvertAnthropicMsg(m apicompat.AnthropicMessage) ([]tkBridgeChatMsg, error) {
	switch m.Role {
	case "user":
		return tkBridgeConvertUserMsg(m.Content)
	case "assistant":
		return tkBridgeConvertAssistantMsg(m.Content)
	default:
		return tkBridgeConvertUserMsg(m.Content)
	}
}

func tkBridgeConvertUserMsg(content json.RawMessage) ([]tkBridgeChatMsg, error) {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return []tkBridgeChatMsg{{
			Role:    "user",
			Content: json.RawMessage(tkBridgeMustMarshalStr(s)),
		}}, nil
	}

	var blocks []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, err
	}

	var msgs []tkBridgeChatMsg

	for _, b := range blocks {
		if b.Type == "tool_result" {
			output := tkBridgeExtractToolResultText(b)
			msgs = append(msgs, tkBridgeChatMsg{
				Role:       "tool",
				Content:    json.RawMessage(tkBridgeMustMarshalStr(output)),
				ToolCallID: b.ToolUseID,
			})
		}
	}

	var parts []apicompat.ChatContentPart
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, apicompat.ChatContentPart{Type: "text", Text: b.Text})
			}
		case "image":
			if b.Source != nil && b.Source.Data != "" {
				mt := b.Source.MediaType
				if mt == "" {
					mt = "image/png"
				}
				parts = append(parts, apicompat.ChatContentPart{
					Type:     "image_url",
					ImageURL: &apicompat.ChatImageURL{URL: "data:" + mt + ";base64," + b.Source.Data},
				})
			}
		}
	}

	if len(parts) > 0 {
		pj, err := json.Marshal(parts)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, tkBridgeChatMsg{Role: "user", Content: pj})
	}

	return msgs, nil
}

func tkBridgeConvertAssistantMsg(content json.RawMessage) ([]tkBridgeChatMsg, error) {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return []tkBridgeChatMsg{{
			Role:    "assistant",
			Content: json.RawMessage(tkBridgeMustMarshalStr(s)),
		}}, nil
	}

	var blocks []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil, err
	}

	msg := tkBridgeChatMsg{Role: "assistant"}

	var textParts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			textParts = append(textParts, b.Text)
		}
	}
	if len(textParts) > 0 {
		msg.Content = json.RawMessage(tkBridgeMustMarshalStr(strings.Join(textParts, "")))
	}

	for _, b := range blocks {
		if b.Type == "tool_use" {
			args := "{}"
			if len(b.Input) > 0 {
				args = string(b.Input)
			}
			msg.ToolCalls = append(msg.ToolCalls, tkBridgeTC{
				ID: b.ID, Type: "function",
				Function: tkBridgeFnCall{Name: b.Name, Arguments: args},
			})
		}
	}

	return []tkBridgeChatMsg{msg}, nil
}

func tkBridgeExtractToolResultText(b apicompat.AnthropicContentBlock) string {
	if len(b.Content) == 0 {
		return "(empty)"
	}
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		if s == "" {
			return "(empty)"
		}
		return s
	}
	var inner []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(b.Content, &inner); err != nil {
		return "(empty)"
	}
	var parts []string
	for _, ib := range inner {
		if ib.Type == "text" && ib.Text != "" {
			parts = append(parts, ib.Text)
		}
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, "\n\n")
}

func tkBridgeConvertTools(tools []apicompat.AnthropicTool) []map[string]any {
	var out []map[string]any
	for _, t := range tools {
		if strings.HasPrefix(t.Type, "web_search") {
			continue
		}
		params := t.InputSchema
		if len(params) == 0 || string(params) == "null" {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  json.RawMessage(params),
			},
		})
	}
	return out
}

func tkBridgeConvertToolChoice(raw json.RawMessage) (any, error) {
	var tc struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil, err
	}
	switch tc.Type {
	case "auto":
		return "auto", nil
	case "any":
		return "required", nil
	case "none":
		return "none", nil
	case "tool":
		return map[string]any{
			"type":     "function",
			"function": map[string]string{"name": tc.Name},
		}, nil
	default:
		return "auto", nil
	}
}

func tkBridgeMustMarshalStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ---------------------------------------------------------------------------
// Bridge capture writer (implements gin.ResponseWriter)
// ---------------------------------------------------------------------------

type bridgeCaptureWriter struct {
	header     http.Header
	buf        *bytes.Buffer
	statusCode int
	written    bool
	size       int
}

func newBridgeCaptureWriter(buf *bytes.Buffer) *bridgeCaptureWriter {
	return &bridgeCaptureWriter{header: make(http.Header), buf: buf, statusCode: 200}
}

func (w *bridgeCaptureWriter) Header() http.Header  { return w.header }
func (w *bridgeCaptureWriter) WriteHeader(code int) { w.statusCode = code; w.written = true }
func (w *bridgeCaptureWriter) Write(data []byte) (int, error) {
	w.written = true
	n, e := w.buf.Write(data)
	w.size += n
	return n, e
}
func (w *bridgeCaptureWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *bridgeCaptureWriter) Status() int                       { return w.statusCode }
func (w *bridgeCaptureWriter) Size() int                         { return w.size }
func (w *bridgeCaptureWriter) Written() bool                     { return w.written }
func (w *bridgeCaptureWriter) WriteHeaderNow()                   {}
func (w *bridgeCaptureWriter) Flush()                            {}
func (w *bridgeCaptureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, fmt.Errorf("not supported")
}
func (w *bridgeCaptureWriter) CloseNotify() <-chan bool { return make(chan bool) } //nolint:staticcheck
func (w *bridgeCaptureWriter) Pusher() http.Pusher      { return nil }

// ---------------------------------------------------------------------------
// Chat Completions buffer → Anthropic SSE conversion
// ---------------------------------------------------------------------------

func convertBufferedChatCompletionsToAnthropicSSE(
	c *gin.Context, buf *bytes.Buffer,
	originalModel, billingModel, upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	state := &tkBridgeChatToAnthState{model: originalModel, toolCallIdx: make(map[int]int)}
	var usage OpenAIUsage

	scanner := bufio.NewScanner(buf)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk apicompat.ChatCompletionsChunk
		if err := json.Unmarshal([]byte(line[6:]), &chunk); err != nil {
			continue
		}

		events := tkBridgeChatChunkToAnthropicEvents(&chunk, state)
		for _, evt := range events {
			if sse, err := apicompat.ResponsesAnthropicEventToSSE(evt); err == nil {
				if _, err := fmt.Fprint(c.Writer, sse); err != nil {
					return &OpenAIForwardResult{Model: originalModel, BillingModel: billingModel, UpstreamModel: upstreamModel, Stream: true, Duration: time.Since(startTime)}, nil
				}
			}
		}
		if len(events) > 0 {
			c.Writer.Flush()
		}

		if chunk.Usage != nil {
			usage = OpenAIUsage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
			if chunk.Usage.PromptTokensDetails != nil {
				usage.CacheReadInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
		}
	}

	for _, evt := range tkBridgeFinalizeChatToAnthStream(state, usage) {
		if sse, err := apicompat.ResponsesAnthropicEventToSSE(evt); err == nil {
			fmt.Fprint(c.Writer, sse) //nolint:errcheck
		}
	}
	c.Writer.Flush()

	return &OpenAIForwardResult{
		Model: originalModel, BillingModel: billingModel, UpstreamModel: upstreamModel,
		Stream: true, Duration: time.Since(startTime), Usage: usage,
	}, nil
}

// ---------------------------------------------------------------------------
// Chat Completions buffer → Anthropic JSON conversion
// ---------------------------------------------------------------------------

func convertBufferedChatCompletionsToAnthropicJSON(
	c *gin.Context, buf *bytes.Buffer,
	originalModel, billingModel, upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	var contentText strings.Builder
	type accTC struct {
		ID   string
		Name string
		Args strings.Builder
	}
	var toolCalls []*accTC
	tcIdxMap := make(map[int]int)
	var finishReason string
	var usage OpenAIUsage

	scanner := bufio.NewScanner(buf)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk apicompat.ChatCompletionsChunk
		if err := json.Unmarshal([]byte(line[6:]), &chunk); err != nil {
			continue
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != nil {
				_, _ = contentText.WriteString(*ch.Delta.Content)
			}
			for _, tc := range ch.Delta.ToolCalls {
				if tc.ID != "" {
					idx := len(toolCalls)
					if tc.Index != nil {
						tcIdxMap[*tc.Index] = idx
					}
					toolCalls = append(toolCalls, &accTC{ID: tc.ID, Name: tc.Function.Name})
				}
				if tc.Function.Arguments != "" {
					ai := len(toolCalls) - 1
					if tc.Index != nil {
						if v, ok := tcIdxMap[*tc.Index]; ok {
							ai = v
						}
					}
					if ai >= 0 && ai < len(toolCalls) {
						_, _ = toolCalls[ai].Args.WriteString(tc.Function.Arguments)
					}
				}
			}
			if ch.FinishReason != nil {
				finishReason = *ch.FinishReason
			}
		}
		if chunk.Usage != nil {
			usage = OpenAIUsage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
			if chunk.Usage.PromptTokensDetails != nil {
				usage.CacheReadInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
			}
		}
	}

	var content []apicompat.AnthropicContentBlock
	if contentText.Len() > 0 {
		content = append(content, apicompat.AnthropicContentBlock{Type: "text", Text: contentText.String()})
	}
	for _, tc := range toolCalls {
		args := tc.Args.String()
		if args == "" {
			args = "{}"
		}
		content = append(content, apicompat.AnthropicContentBlock{
			Type: "tool_use", ID: tc.ID, Name: tc.Name,
			Input: json.RawMessage(args),
		})
	}
	if len(content) == 0 {
		content = append(content, apicompat.AnthropicContentBlock{Type: "text", Text: ""})
	}

	stopReason := "end_turn"
	switch finishReason {
	case "length":
		stopReason = "max_tokens"
	case "tool_calls":
		stopReason = "tool_use"
	}

	resp := apicompat.AnthropicResponse{
		ID:   fmt.Sprintf("msg_bridge_%d", time.Now().UnixNano()),
		Type: "message", Role: "assistant",
		Content: content, Model: originalModel,
		StopReason: stopReason,
		Usage: apicompat.AnthropicUsage{
			InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		},
	}
	c.JSON(http.StatusOK, resp)

	return &OpenAIForwardResult{
		Model: originalModel, BillingModel: billingModel, UpstreamModel: upstreamModel,
		Stream: false, Duration: time.Since(startTime), Usage: usage,
	}, nil
}

// ---------------------------------------------------------------------------
// Streaming Chat Completions chunk → Anthropic SSE events (stateful)
// ---------------------------------------------------------------------------

type tkBridgeChatToAnthState struct {
	model        string
	msgStartSent bool
	msgStopSent  bool
	blockOpen    bool
	blockIndex   int
	blockType    string
	toolCallIdx  map[int]int
	lastFinish   string
}

func tkBridgeChatChunkToAnthropicEvents(chunk *apicompat.ChatCompletionsChunk, st *tkBridgeChatToAnthState) []apicompat.AnthropicStreamEvent {
	var events []apicompat.AnthropicStreamEvent

	if !st.msgStartSent {
		st.msgStartSent = true
		events = append(events, apicompat.AnthropicStreamEvent{
			Type: "message_start",
			Message: &apicompat.AnthropicResponse{
				ID: chunk.ID, Type: "message", Role: "assistant",
				Content: []apicompat.AnthropicContentBlock{},
				Model:   st.model,
				Usage:   apicompat.AnthropicUsage{},
			},
		})
	}

	for _, ch := range chunk.Choices {
		if ch.Delta.Content != nil && *ch.Delta.Content != "" {
			if !st.blockOpen || st.blockType != "text" {
				events = append(events, tkBridgeCloseBlock(st)...)
				idx := st.blockIndex
				st.blockOpen = true
				st.blockType = "text"
				events = append(events, apicompat.AnthropicStreamEvent{
					Type: "content_block_start", Index: &idx,
					ContentBlock: &apicompat.AnthropicContentBlock{Type: "text", Text: ""},
				})
			}
			idx := st.blockIndex
			events = append(events, apicompat.AnthropicStreamEvent{
				Type: "content_block_delta", Index: &idx,
				Delta: &apicompat.AnthropicDelta{Type: "text_delta", Text: *ch.Delta.Content},
			})
		}

		for _, tc := range ch.Delta.ToolCalls {
			if tc.ID != "" {
				events = append(events, tkBridgeCloseBlock(st)...)
				idx := st.blockIndex
				if tc.Index != nil {
					st.toolCallIdx[*tc.Index] = idx
				}
				st.blockOpen = true
				st.blockType = "tool_use"
				events = append(events, apicompat.AnthropicStreamEvent{
					Type: "content_block_start", Index: &idx,
					ContentBlock: &apicompat.AnthropicContentBlock{
						Type: "tool_use", ID: tc.ID, Name: tc.Function.Name,
						Input: json.RawMessage("{}"),
					},
				})
			}
			if tc.Function.Arguments != "" {
				bi := st.blockIndex
				if tc.Index != nil {
					if v, ok := st.toolCallIdx[*tc.Index]; ok {
						bi = v
					}
				}
				events = append(events, apicompat.AnthropicStreamEvent{
					Type: "content_block_delta", Index: &bi,
					Delta: &apicompat.AnthropicDelta{Type: "input_json_delta", PartialJSON: tc.Function.Arguments},
				})
			}
		}

		if ch.FinishReason != nil {
			st.lastFinish = *ch.FinishReason
		}
	}

	return events
}

func tkBridgeCloseBlock(st *tkBridgeChatToAnthState) []apicompat.AnthropicStreamEvent {
	if !st.blockOpen {
		return nil
	}
	idx := st.blockIndex
	st.blockOpen = false
	st.blockIndex++
	return []apicompat.AnthropicStreamEvent{{Type: "content_block_stop", Index: &idx}}
}

func tkBridgeFinalizeChatToAnthStream(st *tkBridgeChatToAnthState, usage OpenAIUsage) []apicompat.AnthropicStreamEvent {
	if !st.msgStartSent || st.msgStopSent {
		return nil
	}
	st.msgStopSent = true

	var events []apicompat.AnthropicStreamEvent
	events = append(events, tkBridgeCloseBlock(st)...)

	stopReason := "end_turn"
	switch st.lastFinish {
	case "length":
		stopReason = "max_tokens"
	case "tool_calls":
		stopReason = "tool_use"
	}

	events = append(events,
		apicompat.AnthropicStreamEvent{
			Type:  "message_delta",
			Delta: &apicompat.AnthropicDelta{StopReason: stopReason},
			Usage: &apicompat.AnthropicUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens},
		},
		apicompat.AnthropicStreamEvent{Type: "message_stop"},
	)
	return events
}
