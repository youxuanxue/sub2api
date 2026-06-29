package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

// kiroBlockKind identifies which content-block type is currently open.
type kiroBlockKind int

const (
	kiroBlockNone kiroBlockKind = iota
	kiroBlockRedactedThinking
	kiroBlockText
	kiroBlockToolUse
)

// kiroSSEEncoder writes the canonical Anthropic /v1/messages SSE event sequence
// from Kiro streaming callbacks. It is NOT safe for concurrent use; callers
// serialize writes (the Kiro callbacks are driven from a single goroutine, but
// kiro_gateway_service.go additionally guards with a mutex).
type kiroSSEEncoder struct {
	w       io.Writer
	flusher http.Flusher
	model   string
	msgID   string

	// inputTokens is the locally-estimated prompt token count (Kiro upstream
	// reports none). It is computable from the request up-front, so the caller
	// sets it before streaming starts and writeMessageStart emits it in
	// message_start.usage.input_tokens. Without this the streamed SSE carried
	// input_tokens=0, and the prod relay (which bills off the parsed SSE usage)
	// recorded every streamed Kiro request at input=0 → systematic under-billing.
	inputTokens int

	started           bool          // message_start has been emitted
	openBlock         kiroBlockKind // currently open content block
	blockIndex        int           // index of the currently open / next block
	stopReason        string        // accumulated stop reason ("end_turn" / "tool_use")
	emittedText       bool          // any text/thinking/tool block emitted
	pendingThinking   strings.Builder
	redactedThinkingEmitted bool
}

func (e *kiroSSEEncoder) writeEvent(eventType string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, writeErr := fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", eventType, data)
	if writeErr == nil && e.flusher != nil {
		e.flusher.Flush()
	}
}

func (e *kiroSSEEncoder) writeMessageStart() {
	if e.started {
		return
	}
	e.started = true
	e.stopReason = "end_turn"
	e.writeEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            e.msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         e.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  e.inputTokens,
				"output_tokens": 0,
			},
		},
	})
}

// flushRedactedThinking emits a single opaque redacted_thinking block for all
// reasoning accumulated so far. Matches Anthropic OAuth + redact-thinking: the
// client sees encrypted-style data, not plaintext thinking_delta events.
func (e *kiroSSEEncoder) flushRedactedThinking() {
	if e.redactedThinkingEmitted || e.pendingThinking.Len() == 0 {
		return
	}
	e.writeMessageStart()
	if e.openBlock != kiroBlockNone {
		e.closeOpenBlock()
	}

	data := kiroproto.RedactedThinkingData(e.pendingThinking.String())
	e.writeEvent("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": e.blockIndex,
		"content_block": map[string]any{
			"type": "redacted_thinking",
			"data": data,
		},
	})
	e.writeEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": e.blockIndex,
	})
	e.openBlock = kiroBlockNone
	e.blockIndex++
	e.emittedText = true
	e.redactedThinkingEmitted = true
}

// ensureBlock opens a content block of the requested kind, closing any
// previously open block of a different kind first. It lazily emits message_start
// on first content so that an upstream failure occurring before any content
// arrives leaves enc.started == false (the caller's `!enc.started` guard then
// surfaces the error instead of closing out a clean empty 200 stream).
func (e *kiroSSEEncoder) ensureBlock(kind kiroBlockKind) {
	if e.openBlock == kind {
		return
	}
	if kind != kiroBlockRedactedThinking {
		e.flushRedactedThinking()
	}
	e.writeMessageStart()
	e.closeOpenBlock()

	var cb map[string]any
	switch kind {
	case kiroBlockText:
		cb = map[string]any{"type": "text", "text": ""}
	default:
		return
	}
	e.writeEvent("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         e.blockIndex,
		"content_block": cb,
	})
	e.openBlock = kind
	e.emittedText = true
}

func (e *kiroSSEEncoder) writeTextDelta(text string) {
	if text == "" {
		return
	}
	e.ensureBlock(kiroBlockText)
	e.writeEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": e.blockIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

func (e *kiroSSEEncoder) writeThinkingDelta(text string) {
	if text == "" {
		return
	}
	e.pendingThinking.WriteString(text)
}

// writeToolUse emits a complete tool_use block (start + input_json_delta + stop).
// Kiro delivers tool uses whole (not incrementally), so the input is serialized
// as a single partial_json delta.
func (e *kiroSSEEncoder) writeToolUse(tu kiroproto.KiroToolUse) {
	e.flushRedactedThinking()
	e.writeMessageStart()
	e.closeOpenBlock()
	e.stopReason = "tool_use"

	e.writeEvent("content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": e.blockIndex,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    tu.ToolUseID,
			"name":  tu.Name,
			"input": map[string]any{},
		},
	})
	e.openBlock = kiroBlockToolUse
	e.emittedText = true

	input := tu.Input
	if input == nil {
		input = map[string]any{}
	}
	if jsonBytes, err := json.Marshal(input); err == nil {
		e.writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": e.blockIndex,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": string(jsonBytes)},
		})
	}
	e.closeOpenBlock()
}

// closeOpenBlock emits content_block_stop for the currently open block, if any,
// and advances the block index.
func (e *kiroSSEEncoder) closeOpenBlock() {
	if e.openBlock == kiroBlockNone {
		return
	}
	e.writeEvent("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": e.blockIndex,
	})
	e.openBlock = kiroBlockNone
	e.blockIndex++
}

func (e *kiroSSEEncoder) writeMessageDelta(outputTokens int) {
	e.flushRedactedThinking()
	stop := e.stopReason
	if stop == "" {
		stop = "end_turn"
	}
	e.writeEvent("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stop,
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": outputTokens},
	})
}

func (e *kiroSSEEncoder) writeMessageStop() {
	e.writeEvent("message_stop", map[string]any{"type": "message_stop"})
}
