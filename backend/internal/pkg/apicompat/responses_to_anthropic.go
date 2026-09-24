package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Non-streaming: ResponsesResponse → AnthropicResponse
// ---------------------------------------------------------------------------

// ResponsesToAnthropic converts a Responses API response directly into an
// Anthropic Messages response. Reasoning output items are mapped to thinking
// blocks; function_call items become tool_use blocks.
func ResponsesToAnthropic(resp *ResponsesResponse, model string) *AnthropicResponse {
	out := &AnthropicResponse{
		ID:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
	}

	var blocks []AnthropicContentBlock

	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			// Always surface encrypted_content as thinking.signature so Claude
			// Code / multi-turn clients can send it back. Signature-only
			// thinking blocks are valid when the model omits a visible summary.
			thinkingText := extractResponsesOutputReasoningText(&item)
			if thinkingText != "" || strings.TrimSpace(item.EncryptedContent) != "" {
				blocks = append(blocks, AnthropicContentBlock{
					Type:      "thinking",
					Thinking:  thinkingText,
					Signature: item.EncryptedContent,
				})
			}
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" && part.Text != "" {
					blocks = append(blocks, AnthropicContentBlock{
						Type: "text",
						Text: part.Text,
					})
				}
			}
		case "function_call":
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(item.CallID),
				Name:  item.Name,
				Input: sanitizeAnthropicToolUseInput(item.Name, item.Arguments),
			})
		case "web_search_call":
			toolUseID := "srvtoolu_" + item.ID
			query := ""
			if item.Action != nil {
				query = item.Action.Query
			}
			inputJSON, _ := json.Marshal(map[string]string{"query": query})
			blocks = append(blocks, AnthropicContentBlock{
				Type:  "server_tool_use",
				ID:    toolUseID,
				Name:  "web_search",
				Input: inputJSON,
			})
			emptyResults, _ := json.Marshal([]struct{}{})
			blocks = append(blocks, AnthropicContentBlock{
				Type:      "web_search_tool_result",
				ToolUseID: toolUseID,
				Content:   emptyResults,
			})
		}
	}

	if len(blocks) == 0 {
		blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: ""})
	}
	out.Content = blocks

	out.StopReason = AnthropicStopReasonPtr(responsesStatusToAnthropicStopReason(resp.Status, resp.IncompleteDetails, blocks))

	if resp.Usage != nil {
		out.Usage = anthropicUsageFromResponsesUsage(resp.Usage)
	}

	return out
}

func anthropicUsageFromResponsesUsage(usage *ResponsesUsage) AnthropicUsage {
	if usage == nil {
		return AnthropicUsage{}
	}

	cachedTokens := 0
	if usage.InputTokensDetails != nil {
		cachedTokens = usage.InputTokensDetails.CachedTokens
	}

	inputTokens := usage.InputTokens - cachedTokens - usage.CacheCreationInputTokens
	if inputTokens < 0 {
		inputTokens = 0
	}

	return AnthropicUsage{
		InputTokens:              inputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheReadInputTokens:     cachedTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
	}
}

// details is retained in the signature so callers need not change; the reason
// value no longer drives dispatch — every "incomplete" maps to "max_tokens".
func responsesStatusToAnthropicStopReason(status string, details *ResponsesIncompleteDetails, blocks []AnthropicContentBlock) string {
	switch status {
	case "incomplete":
		// Any "incomplete" terminal means the upstream cut us off — whether the
		// reason is max_output_tokens, content_filter, server_error, or anything
		// else. Mapping the non-budget reasons to "end_turn" used to make Claude
		// Code's agentic loop think the task finished naturally and stop, even
		// though there is more work to do. "max_tokens" is the only Anthropic
		// stop reason that signals "cut off, continue is sensible" — use it for
		// every incomplete case so the client can recover. The original reason
		// (content_filter, server_error, …) is surfaced via the gateway's
		// access log instead.
		return "max_tokens"
	case "completed":
		if containsAnthropicToolUseBlock(blocks) {
			return "tool_use"
		}
		return "end_turn"
	default:
		return "end_turn"
	}
}

func containsAnthropicToolUseBlock(blocks []AnthropicContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

func sanitizeAnthropicToolUseInput(name string, raw string) json.RawMessage {
	if name != "Read" || raw == "" {
		return json.RawMessage(raw)
	}

	var input map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return json.RawMessage(raw)
	}

	if pages, ok := input["pages"]; !ok || string(pages) != `""` {
		return json.RawMessage(raw)
	}

	delete(input, "pages")
	sanitized, err := json.Marshal(input)
	if err != nil {
		return json.RawMessage(raw)
	}
	return sanitized
}

// ---------------------------------------------------------------------------
// Streaming: ResponsesStreamEvent → []AnthropicStreamEvent (stateful converter)
// ---------------------------------------------------------------------------

// responsesTextPart identifies one output_text part of a streamed response.
type responsesTextPart struct {
	OutputIndex  int
	ContentIndex int
}

// ResponsesEventToAnthropicState tracks state for converting a sequence of
// Responses SSE events directly into Anthropic SSE events.
type ResponsesEventToAnthropicState struct {
	MessageStartSent bool
	MessageStopSent  bool

	ContentBlockIndex   int
	ContentBlockOpen    bool
	CurrentBlockType    string // "text" | "thinking" | "tool_use"
	CurrentToolName     string
	CurrentToolArgs     string
	CurrentToolHadDelta bool
	HasToolCall         bool
	// EmittedAnyContentBlock protects Anthropic SSE clients from streams that
	// terminate without at least one content block.
	EmittedAnyContentBlock bool
	// PendingThinkingSignature is filled from reasoning.encrypted_content and
	// emitted as signature_delta before the thinking block is closed.
	PendingThinkingSignature string
	// ReasoningTextEmitted 避免 output_item.done 在已有 delta 时重复抄 thinking。
	ReasoningTextEmitted bool
	// TextEmitted 避免 response.completed.output 在已有 text delta 时重复抄正文。
	TextEmitted bool

	// OutputIndexToBlockIdx maps Responses output_index → Anthropic content block index.
	OutputIndexToBlockIdx map[int]int

	// textByPart records the text already delivered for each output_text part
	// so that a done payload can be reconciled against it. It outlives the
	// content block; closeCurrentBlock must not reset it.
	textByPart map[responsesTextPart]*strings.Builder
	// textDelivered records whether any assistant text reached the client.
	textDelivered bool

	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int

	ResponseID string
	Model      string
	Created    int64

	// StopReason is the Anthropic-shaped stop reason emitted by the most recent
	// terminal event (response.completed / response.incomplete / etc.). The
	// gateway forwards this value to the access log so we can verify the
	// stop_reason mapping fix is behaving as designed.
	StopReason string
	// IncompleteReason mirrors the upstream incomplete_details.reason verbatim
	// (max_output_tokens, content_filter, server_error, …) so we can tell
	// which kind of cutoff produced a "max_tokens" stop_reason. Empty when the
	// response did not terminate as incomplete.
	IncompleteReason string
}

// NewResponsesEventToAnthropicState returns an initialised stream state.
func NewResponsesEventToAnthropicState() *ResponsesEventToAnthropicState {
	return &ResponsesEventToAnthropicState{
		OutputIndexToBlockIdx: make(map[int]int),
		textByPart:            make(map[responsesTextPart]*strings.Builder),
		Created:               time.Now().Unix(),
	}
}

// ResponsesEventToAnthropicEvents converts a single Responses SSE event into
// zero or more Anthropic SSE events, updating state as it goes.
func ResponsesEventToAnthropicEvents(
	evt *ResponsesStreamEvent,
	state *ResponsesEventToAnthropicState,
) []AnthropicStreamEvent {
	switch evt.Type {
	case "response.created":
		return resToAnthHandleCreated(evt, state)
	case "response.output_item.added":
		return resToAnthHandleOutputItemAdded(evt, state)
	case "response.output_text.delta":
		return resToAnthHandleTextDelta(evt, state)
	case "response.output_text.done":
		return resToAnthHandleTextDone(evt, state)
	case "response.function_call_arguments.delta",
		// custom/freeform 工具的输入增量与 function_call 参数增量同形。
		"response.custom_tool_call_input.delta":
		return resToAnthHandleFuncArgsDelta(evt, state)
	case "response.function_call_arguments.done":
		return resToAnthHandleFuncArgsDone(evt, state)
	case "response.output_item.done":
		return resToAnthHandleOutputItemDone(evt, state)
	case "response.reasoning_summary_part.added":
		return resToAnthEnsureReasoningBlockOpen(state, evt.OutputIndex)
	case "response.reasoning_summary_part.done",
		"response.reasoning_summary_text.done",
		// reasoning_text.done 与 summary 结束帧同形：只标记片段结束，不关 thinking。
		"response.reasoning_text.done":
		// Keep the thinking block open until response.output_item.done.
		// gpt-5.6-terra (and Grok/Codex) may emit more reasoning_* deltas after a
		// summary part finishes; closing here produced orphan thinking_delta
		// events after content_block_stop, which Claude CLI rejects as a
		// malformed stream ("no response was produced"). Encrypted signatures
		// also arrive on output_item.done — closing early would drop them.
		return nil
	case "response.reasoning_summary_text.delta",
		// 原始推理文本增量，与 reasoning summary 一样映射为 thinking。
		"response.reasoning_text.delta":
		return resToAnthHandleReasoningDelta(evt, state)
	// response.done 是 Realtime/WS 与项目透传路径使用的终止别名；
	// 普通 Responses HTTP SSE 的公开终止事件仍以 response.completed 为主。
	case "response.completed", "response.done", "response.incomplete", "response.failed":
		return resToAnthHandleCompleted(evt, state)
	default:
		return nil
	}
}

// FinalizeResponsesAnthropicStream emits synthetic termination events if the
// stream ended without a proper completion event.
func FinalizeResponsesAnthropicStream(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.MessageStartSent || state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, closeCurrentBlock(state)...)
	// US-027: same schema firewall as resToAnthHandleCompleted, but for the
	// premature-stream-end path (upstream cut us off before sending a terminal
	// event).
	events = append(events, ensureContentBlockEmittedAsEmptyText(state)...)

	stopReason := "end_turn"
	if state.HasToolCall {
		stopReason = "tool_use"
	}

	events = append(events,
		AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:              state.InputTokens,
				OutputTokens:             state.OutputTokens,
				CacheReadInputTokens:     state.CacheReadInputTokens,
				CacheCreationInputTokens: state.CacheCreationInputTokens,
			},
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	state.MessageStopSent = true
	return events
}

// ensureContentBlockEmittedAsEmptyText injects a single empty-text content block
// when the streaming state has produced no real content blocks. Used by both
// terminal handlers (resToAnthHandleCompleted + FinalizeResponsesAnthropicStream)
// to guarantee the Anthropic SSE stream never closes with zero content blocks
// — see US-027 / anthropics/claude-code#24662.
//
// No-op when state.EmittedAnyContentBlock is true (the normal happy path).
func ensureContentBlockEmittedAsEmptyText(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.EmittedAnyContentBlock {
		return nil
	}
	idx := state.ContentBlockIndex
	state.ContentBlockIndex++
	state.EmittedAnyContentBlock = true
	return []AnthropicStreamEvent{
		{
			Type:         "content_block_start",
			Index:        &idx,
			ContentBlock: &AnthropicContentBlock{Type: "text", Text: ""},
		},
		{
			Type:  "content_block_stop",
			Index: &idx,
		},
	}
}

// ResponsesAnthropicEventToSSE formats an AnthropicStreamEvent as an SSE line pair.
func ResponsesAnthropicEventToSSE(evt AnthropicStreamEvent) (string, error) {
	data, err := json.Marshal(evt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", evt.Type, data), nil
}

// --- internal handlers ---

func resToAnthHandleCreated(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	return resToAnthEnsureMessageStart(state, evt)
}

func resToAnthEnsureMessageStart(state *ResponsesEventToAnthropicState, evt *ResponsesStreamEvent) []AnthropicStreamEvent {
	if evt != nil && evt.Response != nil {
		if id := strings.TrimSpace(evt.Response.ID); id != "" {
			state.ResponseID = id
		}
		// Only use upstream model if no override was set (e.g. originalModel)
		if state.Model == "" {
			state.Model = evt.Response.Model
		}
	}

	if state.MessageStartSent {
		return nil
	}
	state.MessageStartSent = true

	// Official Anthropic message_start uses stop_reason: null and usage with
	// input_tokens when known. We leave StopReason nil (JSON null) and usage
	// zeros until response.completed; never emit stop_reason:"" which breaks
	// strict clients' turn-finalization / session usage accounting.
	return []AnthropicStreamEvent{{
		Type: "message_start",
		Message: &AnthropicResponse{
			ID:         state.ResponseID,
			Type:       "message",
			Role:       "assistant",
			Content:    []AnthropicContentBlock{},
			Model:      state.Model,
			StopReason: nil,
			Usage: AnthropicUsage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		},
	}}
}

func resToAnthBackfillCompletedOutput(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt == nil || evt.Response == nil {
		return nil
	}

	var events []AnthropicStreamEvent
	for _, item := range evt.Response.Output {
		switch item.Type {
		case "reasoning":
			if state.ReasoningTextEmitted {
				if sig := strings.TrimSpace(item.EncryptedContent); sig != "" && state.PendingThinkingSignature == "" {
					state.PendingThinkingSignature = sig
				}
				continue
			}
			text := extractResponsesOutputReasoningText(&item)
			if text == "" && strings.TrimSpace(item.EncryptedContent) == "" {
				continue
			}
			events = append(events, resToAnthEnsureReasoningBlockOpen(state, 0)...)
			if text != "" {
				blockIdx := state.OutputIndexToBlockIdx[0]
				events = append(events, AnthropicStreamEvent{
					Type:  "content_block_delta",
					Index: &blockIdx,
					Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: text},
				})
				state.ReasoningTextEmitted = true
			}
			if sig := strings.TrimSpace(item.EncryptedContent); sig != "" {
				state.PendingThinkingSignature = sig
			}
			events = append(events, closeCurrentBlock(state)...)
		case "message":
			// Text recovery for terminal message items is handled by resToAnthRecoverTerminalText
			// to properly aggregate all items and avoid duplicate delivery.
			continue
		case "function_call", "custom_tool_call":
			if state.HasToolCall {
				continue
			}
			events = append(events, resToAnthHandleOutputItemAdded(&ResponsesStreamEvent{
				Item: &item,
			}, state)...)
			if item.Arguments != "" {
				events = append(events, resToAnthHandleFuncArgsDelta(&ResponsesStreamEvent{Delta: item.Arguments}, state)...)
			}
			events = append(events, closeCurrentBlock(state)...)
		}
	}
	return events
}

func resToAnthHandleOutputItemAdded(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	switch evt.Item.Type {
	// function_call 与 custom_tool_call（custom/freeform 工具，如新版 apply_patch）
	// 同样映射为 Anthropic 的 tool_use 块。
	case "function_call", "custom_tool_call":
		var events []AnthropicStreamEvent
		events = append(events, closeCurrentBlock(state)...)

		idx := state.ContentBlockIndex
		state.OutputIndexToBlockIdx[evt.OutputIndex] = idx
		state.ContentBlockOpen = true
		state.CurrentBlockType = "tool_use"
		state.CurrentToolName = evt.Item.Name
		state.CurrentToolArgs = ""
		state.EmittedAnyContentBlock = true
		state.CurrentToolHadDelta = false
		state.HasToolCall = true

		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type:  "tool_use",
				ID:    fromResponsesCallID(evt.Item.CallID),
				Name:  evt.Item.Name,
				Input: json.RawMessage("{}"),
			},
		})
		return events

	case "reasoning":
		// Open thinking immediately so /v1/messages clients (claude-cli) receive
		// content_block_start during long upstream reasoning before the first delta.
		events := resToAnthEnsureReasoningBlockOpen(state, evt.OutputIndex)
		if sig := strings.TrimSpace(evt.Item.EncryptedContent); sig != "" {
			state.PendingThinkingSignature = sig
		}
		return events

	case "message":
		return nil
	}

	return nil
}

func resToAnthHandleTextDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	return resToAnthEmitText(evt.Delta, resToAnthTextPartOf(evt), state)
}

func resToAnthTextPartOf(evt *ResponsesStreamEvent) responsesTextPart {
	return responsesTextPart{OutputIndex: evt.OutputIndex, ContentIndex: evt.ContentIndex}
}

// resToAnthEmitText opens a text block when needed, emits text, and records it
// against its part so that a later payload for the same part is reconciled
// against what the client already received.
func resToAnthEmitText(text string, part responsesTextPart, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if text == "" {
		return nil
	}

	var events []AnthropicStreamEvent
	if !state.ContentBlockOpen || state.CurrentBlockType != "text" {
		events = append(events, closeCurrentBlock(state)...)

		idx := state.ContentBlockIndex
		state.ContentBlockOpen = true
		state.CurrentBlockType = "text"
		state.EmittedAnyContentBlock = true

		events = append(events, AnthropicStreamEvent{
			Type:  "content_block_start",
			Index: &idx,
			ContentBlock: &AnthropicContentBlock{
				Type: "text",
				Text: "",
			},
		})
	}

	delivered, ok := state.textByPart[part]
	if !ok {
		delivered = &strings.Builder{}
		state.textByPart[part] = delivered
	}
	_, _ = delivered.WriteString(text)
	state.textDelivered = true

	idx := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &idx,
		Delta: &AnthropicDelta{
			Type: "text_delta",
			Text: text,
		},
	})
	state.TextEmitted = true
	return events
}

// resToAnthRecoverText emits the tail of a finished text payload that never
// reached the client. Streamed text cannot be recalled, so a payload that does
// not extend what was already delivered is left alone.
func resToAnthRecoverText(text string, part responsesTextPart, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	builder, known := state.textByPart[part]
	if !known && state.textDelivered {
		// The payload is indexed differently from every delta seen so far, so
		// which part it finishes cannot be established. Recovering it could
		// repeat an answer the client already has, which is worse than leaving
		// a partially delivered one alone.
		return nil
	}

	var delivered string
	if known {
		delivered = builder.String()
	}
	if text == delivered || !strings.HasPrefix(text, delivered) {
		return nil
	}
	return resToAnthEmitText(text[len(delivered):], part, state)
}

// resToAnthHandleTextDone recovers text that upstream carried only on the done
// event before closing the block, which some streams use instead of sending
// output_text.delta at all.
func resToAnthHandleTextDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStopSent {
		// The message is already terminated; a late payload cannot be delivered
		// without emitting a content block after message_stop.
		return resToAnthHandleBlockDone(state)
	}

	events := resToAnthRecoverText(evt.Text, resToAnthTextPartOf(evt), state)
	return append(events, resToAnthHandleBlockDone(state)...)
}

func resToAnthHandleFuncArgsDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	if state.CurrentBlockType == "tool_use" && state.CurrentToolName == "Read" {
		state.CurrentToolArgs += evt.Delta
		if state.CurrentToolHadDelta || !json.Valid([]byte(state.CurrentToolArgs)) {
			return nil
		}

		blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
		if !ok {
			return nil
		}
		state.CurrentToolHadDelta = true
		sanitized := sanitizeAnthropicToolUseInput(state.CurrentToolName, state.CurrentToolArgs)
		return []AnthropicStreamEvent{{
			Type:  "content_block_delta",
			Index: &blockIdx,
			Delta: &AnthropicDelta{
				Type:        "input_json_delta",
				PartialJSON: string(sanitized),
			},
		}}
	}

	if state.CurrentBlockType == "tool_use" {
		state.CurrentToolHadDelta = true
	}

	blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
	if !ok {
		return nil
	}

	return []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:        "input_json_delta",
			PartialJSON: evt.Delta,
		},
	}}
}

func resToAnthHandleFuncArgsDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	if state.CurrentBlockType != "tool_use" {
		return resToAnthHandleBlockDone(state)
	}

	raw := evt.Arguments
	if raw == "" {
		raw = state.CurrentToolArgs
	}
	if raw == "" || state.CurrentToolHadDelta {
		return closeCurrentBlock(state)
	}
	if state.CurrentToolName == "Read" {
		sanitized := sanitizeAnthropicToolUseInput(state.CurrentToolName, raw)
		if len(sanitized) == 0 {
			return closeCurrentBlock(state)
		}
		raw = string(sanitized)
	}

	// 从事件的 OutputIndex 解析正确的 block index，与 resToAnthHandleFuncArgsDelta 对齐
	blockIdx, ok := state.OutputIndexToBlockIdx[evt.OutputIndex]
	if !ok {
		blockIdx = state.ContentBlockIndex
	}

	// 如果 block 已关闭（ContentBlockIndex 已越过它），说明 arguments 已通过 delta 流式发完，不再补发
	if !state.ContentBlockOpen || blockIdx != state.ContentBlockIndex {
		return nil
	}

	events := []AnthropicStreamEvent{{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:        "input_json_delta",
			PartialJSON: raw,
		},
	}}
	events = append(events, closeCurrentBlock(state)...)
	return events
}

func resToAnthEnsureReasoningBlockOpen(state *ResponsesEventToAnthropicState, outputIndex int) []AnthropicStreamEvent {
	if blockIdx, ok := state.OutputIndexToBlockIdx[outputIndex]; ok {
		// Mapping is only live while that thinking block is still the open one.
		// A stale map (block already stopped) must reopen; otherwise a late
		// reasoning delta is emitted with no content_block_start.
		if state.ContentBlockOpen && state.CurrentBlockType == "thinking" && state.ContentBlockIndex == blockIdx {
			return nil
		}
		delete(state.OutputIndexToBlockIdx, outputIndex)
	}

	var events []AnthropicStreamEvent
	events = append(events, closeCurrentBlock(state)...)

	blockIdx := state.ContentBlockIndex
	state.OutputIndexToBlockIdx[outputIndex] = blockIdx
	state.ContentBlockOpen = true
	state.CurrentBlockType = "thinking"
	state.EmittedAnyContentBlock = true

	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &blockIdx,
		ContentBlock: &AnthropicContentBlock{
			Type:     "thinking",
			Thinking: "",
		},
	})
	return events
}

func resToAnthHandleReasoningDelta(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Delta == "" {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, resToAnthEnsureReasoningBlockOpen(state, evt.OutputIndex)...)

	blockIdx := state.OutputIndexToBlockIdx[evt.OutputIndex]
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: &blockIdx,
		Delta: &AnthropicDelta{
			Type:     "thinking_delta",
			Thinking: evt.Delta,
		},
	})
	state.ReasoningTextEmitted = true
	return events
}

func resToAnthHandleBlockDone(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	return closeCurrentBlock(state)
}

func resToAnthHandleOutputItemDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if evt.Item == nil {
		return nil
	}

	// Handle web_search_call → synthesize server_tool_use + web_search_tool_result blocks.
	if evt.Item.Type == "web_search_call" && evt.Item.Status == "completed" {
		return resToAnthHandleWebSearchDone(evt, state)
	}

	// Capture encrypted_content on reasoning item done (often only present here).
	if evt.Item.Type == "reasoning" {
		if sig := strings.TrimSpace(evt.Item.EncryptedContent); sig != "" {
			state.PendingThinkingSignature = sig
		}
		if !state.ReasoningTextEmitted {
			if text := extractResponsesOutputReasoningText(evt.Item); text != "" {
				var events []AnthropicStreamEvent
				events = append(events, resToAnthEnsureReasoningBlockOpen(state, evt.OutputIndex)...)
				blockIdx := state.OutputIndexToBlockIdx[evt.OutputIndex]
				events = append(events, AnthropicStreamEvent{
					Type:  "content_block_delta",
					Index: &blockIdx,
					Delta: &AnthropicDelta{
						Type:     "thinking_delta",
						Thinking: text,
					},
				})
				state.ReasoningTextEmitted = true
				events = append(events, closeCurrentBlock(state)...)
				return events
			}
		}
	}

	if state.ContentBlockOpen {
		return closeCurrentBlock(state)
	}
	return nil
}

// resToAnthHandleWebSearchDone converts an OpenAI web_search_call output item
// into Anthropic server_tool_use + web_search_tool_result content block pairs.
// This allows Claude Code to count the searches performed.
func resToAnthHandleWebSearchDone(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	var events []AnthropicStreamEvent
	events = append(events, closeCurrentBlock(state)...)

	toolUseID := "srvtoolu_" + evt.Item.ID
	query := ""
	if evt.Item.Action != nil {
		query = evt.Item.Action.Query
	}
	inputJSON, _ := json.Marshal(map[string]string{"query": query})

	// Emit server_tool_use block (start + stop).
	idx1 := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx1,
		ContentBlock: &AnthropicContentBlock{
			Type:  "server_tool_use",
			ID:    toolUseID,
			Name:  "web_search",
			Input: inputJSON,
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx1,
	})
	state.ContentBlockIndex++
	state.EmittedAnyContentBlock = true

	// Emit web_search_tool_result block (start + stop).
	// Content is empty because OpenAI does not expose individual search results;
	// the model consumes them internally and produces text output.
	emptyResults, _ := json.Marshal([]struct{}{})
	idx2 := state.ContentBlockIndex
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_start",
		Index: &idx2,
		ContentBlock: &AnthropicContentBlock{
			Type:      "web_search_tool_result",
			ToolUseID: toolUseID,
			Content:   emptyResults,
		},
	})
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx2,
	})
	state.ContentBlockIndex++

	return events
}

// resToAnthRecoverTerminalText emits assistant text that only ever appeared in
// the terminal response payload, which some streams populate without sending
// any output_text event.
//
// It only runs when no text at all reached the client. Streamed events and the
// terminal output array carry no guaranteed common identity, so reconciling
// them part by part risks repeating an answer the client already has, which is
// worse than leaving a partially streamed response as it is.
func resToAnthRecoverTerminalText(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.textDelivered || evt.Response == nil {
		return nil
	}

	var events []AnthropicStreamEvent
	for outputIndex, item := range evt.Response.Output {
		if item.Type != "message" {
			continue
		}
		for contentIndex, content := range item.Content {
			if content.Type != "output_text" {
				continue
			}
			part := responsesTextPart{OutputIndex: outputIndex, ContentIndex: contentIndex}
			events = append(events, resToAnthEmitText(content.Text, part, state)...)
		}
	}
	if len(events) > 0 {
		events = append(events, closeCurrentBlock(state)...)
	}
	return events
}

func resToAnthHandleCompleted(evt *ResponsesStreamEvent, state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if state.MessageStopSent {
		return nil
	}

	var events []AnthropicStreamEvent
	events = append(events, resToAnthEnsureMessageStart(state, evt)...)
	// Codex / edge-mirror hops often buffer visible text into the terminal
	// response.output after a long reasoning wait and omit output_text.delta.
	// Backfill while the current thinking block is still open so reasoning
	// text is not written after content_block_stop. Then close leftover
	// blocks before the US-027 empty-text firewall.
	events = append(events, resToAnthBackfillCompletedOutput(evt, state)...)
	events = append(events, closeCurrentBlock(state)...)
	events = append(events, resToAnthRecoverTerminalText(evt, state)...)
	// US-027 schema firewall: a terminal event MUST be preceded by at least one
	// content_block_start/_stop pair. If the upstream stream emitted neither
	// real content nor a synthesizable delta, inject an empty-text block here so
	// Claude Code never persists a content-less message into its session JSONL
	// (anthropics/claude-code#24662). Mirrors the non-streaming fallback in
	// ResponsesToAnthropic (`if len(blocks) == 0 { append empty text }`).
	events = append(events, ensureContentBlockEmittedAsEmptyText(state)...)

	stopReason := "end_turn"
	if evt.Usage != nil {
		usage := anthropicUsageFromResponsesUsage(evt.Usage)
		state.InputTokens = usage.InputTokens
		state.OutputTokens = usage.OutputTokens
		state.CacheReadInputTokens = usage.CacheReadInputTokens
		state.CacheCreationInputTokens = usage.CacheCreationInputTokens
	}
	if evt.Response != nil {
		if evt.Response.Usage != nil {
			usage := anthropicUsageFromResponsesUsage(evt.Response.Usage)
			state.InputTokens = usage.InputTokens
			state.OutputTokens = usage.OutputTokens
			state.CacheReadInputTokens = usage.CacheReadInputTokens
			state.CacheCreationInputTokens = usage.CacheCreationInputTokens
		}
		switch evt.Response.Status {
		case "incomplete":
			// Mirror the non-streaming branch in responsesStatusToAnthropicStopReason:
			// every "incomplete" terminal is a cutoff (max_output_tokens, content_filter,
			// server_error, …). All of them map to "max_tokens" so Claude Code's
			// agentic loop knows the turn was cut short and continuation is sensible.
			// Returning "end_turn" here used to make CC stop after non-budget cutoffs.
			stopReason = "max_tokens"
			if evt.Response.IncompleteDetails != nil {
				state.IncompleteReason = evt.Response.IncompleteDetails.Reason
			}
		case "completed":
			if state.HasToolCall {
				stopReason = "tool_use"
			}
		}
		state.StopReason = stopReason
	}

	events = append(events,
		AnthropicStreamEvent{
			Type: "message_delta",
			Delta: &AnthropicDelta{
				StopReason: stopReason,
			},
			Usage: &AnthropicUsage{
				InputTokens:              state.InputTokens,
				OutputTokens:             state.OutputTokens,
				CacheReadInputTokens:     state.CacheReadInputTokens,
				CacheCreationInputTokens: state.CacheCreationInputTokens,
			},
		},
		AnthropicStreamEvent{Type: "message_stop"},
	)
	state.MessageStopSent = true
	return events
}

func closeCurrentBlock(state *ResponsesEventToAnthropicState) []AnthropicStreamEvent {
	if !state.ContentBlockOpen {
		return nil
	}
	idx := state.ContentBlockIndex
	var events []AnthropicStreamEvent
	// Emit signature_delta before stop so Claude clients retain encrypted
	// reasoning for the next turn (required for Grok multi-turn cache).
	if state.CurrentBlockType == "thinking" {
		if sig := strings.TrimSpace(state.PendingThinkingSignature); sig != "" {
			events = append(events, AnthropicStreamEvent{
				Type:  "content_block_delta",
				Index: &idx,
				Delta: &AnthropicDelta{
					Type:      "signature_delta",
					Signature: sig,
				},
			})
		}
		state.PendingThinkingSignature = ""
	}
	state.ContentBlockOpen = false
	state.ContentBlockIndex++
	// Do NOT clear OutputIndexToBlockIdx here: parallel tool_use blocks keep
	// historical output_index→block maps after stop so a later packed
	// function_call_arguments.done can resolve its own index and skip when
	// blockIdx != ContentBlockIndex. Stale reasoning maps are handled in
	// resToAnthEnsureReasoningBlockOpen (reopen when the mapped block is no
	// longer the live thinking block).
	state.CurrentToolName = ""
	state.CurrentToolArgs = ""
	state.CurrentToolHadDelta = false
	events = append(events, AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: &idx,
	})
	return events
}
