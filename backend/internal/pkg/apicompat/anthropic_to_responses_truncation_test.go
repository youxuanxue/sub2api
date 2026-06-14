package apicompat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// terminalEvent returns the single terminal stream event in events (the one
// whose type is in the response.* terminal set), or nil if none is present.
func terminalEvent(events []ResponsesStreamEvent) *ResponsesStreamEvent {
	for i := range events {
		switch events[i].Type {
		case "response.completed", "response.incomplete", "response.failed", "response.cancelled":
			return &events[i]
		}
	}
	return nil
}

// driveAnthropicResponsesStream feeds a sequence of Anthropic SSE events through
// the forward converter and returns the accumulated Responses events.
func driveAnthropicResponsesStream(state *AnthropicEventToResponsesState, evts ...*AnthropicStreamEvent) []ResponsesStreamEvent {
	var out []ResponsesStreamEvent
	for _, e := range evts {
		out = append(out, AnthropicEventToResponsesEvents(e, state)...)
	}
	return out
}

// TestFinalizeAnthropicResponsesStream_TruncationEmitsIncomplete is the
// regression guard for Wei-Shaw/sub2api#2245: an upstream stream that ends
// without message_stop must be finalized as response.incomplete (status
// "incomplete"), not a fake response.completed that tells a strict Responses
// client the truncated turn succeeded.
func TestFinalizeAnthropicResponsesStream_TruncationEmitsIncomplete(t *testing.T) {
	state := NewAnthropicEventToResponsesState()

	// message_start + some partial content, then the upstream drops (no message_stop).
	driveAnthropicResponsesStream(state,
		&AnthropicStreamEvent{
			Type: "message_start",
			Message: &AnthropicResponse{
				ID:    "msg_truncated",
				Model: "claude-sonnet-4-5-20250929",
				Usage: AnthropicUsage{InputTokens: 12},
			},
		},
		&AnthropicStreamEvent{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "text"}},
		&AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "text_delta", Text: "partial"}},
	)
	require.True(t, state.CreatedSent, "response.created must have been emitted")
	require.False(t, state.CompletedSent, "no terminal event before finalize")

	final := FinalizeAnthropicResponsesStream(state)
	require.NotEmpty(t, final, "finalize must synthesize a terminal event on truncation")

	term := terminalEvent(final)
	require.NotNil(t, term, "a terminal response.* event must be emitted")
	assert.Equal(t, "response.incomplete", term.Type, "truncated stream must emit response.incomplete, not response.completed")
	require.NotNil(t, term.Response)
	assert.Equal(t, "incomplete", term.Response.Status)
	require.NotNil(t, term.Response.IncompleteDetails)
	assert.Equal(t, "interrupted", term.Response.IncompleteDetails.Reason)
	assert.True(t, state.CompletedSent, "finalize must mark the terminal as sent")
}

// TestFinalizeAnthropicResponsesStream_NoTerminalBeforeCreated guards that a
// stream which never started (no response.created) is a no-op on finalize.
func TestFinalizeAnthropicResponsesStream_NoTerminalBeforeCreated(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	assert.Nil(t, FinalizeAnthropicResponsesStream(state))
}

// TestFinalizeAnthropicResponsesStream_NoOpAfterMessageStop guards that a
// cleanly completed stream (message_stop seen) does NOT get a second synthetic
// terminal from finalize.
func TestFinalizeAnthropicResponsesStream_NoOpAfterMessageStop(t *testing.T) {
	state := NewAnthropicEventToResponsesState()

	events := driveAnthropicResponsesStream(state,
		&AnthropicStreamEvent{
			Type: "message_start",
			Message: &AnthropicResponse{
				ID:    "msg_complete",
				Model: "claude-sonnet-4-5-20250929",
				Usage: AnthropicUsage{InputTokens: 12},
			},
		},
		&AnthropicStreamEvent{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "text"}},
		&AnthropicStreamEvent{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "text_delta", Text: "done"}},
		&AnthropicStreamEvent{Type: "message_stop"},
	)

	// The clean completion must surface as response.completed.
	term := terminalEvent(events)
	require.NotNil(t, term, "message_stop must produce a terminal event")
	assert.Equal(t, "response.completed", term.Type)
	require.NotNil(t, term.Response)
	assert.Equal(t, "completed", term.Response.Status)

	// Finalize after a clean completion is a no-op (no duplicate terminal).
	assert.Nil(t, FinalizeAnthropicResponsesStream(state),
		"finalize must not emit a second terminal after message_stop")
}

// TestResponsesTerminalEventTypeForStatus pins the status→event-type mapping so
// the streaming terminal event type can never disagree with response.status.
func TestResponsesTerminalEventTypeForStatus(t *testing.T) {
	cases := map[string]string{
		"completed":  "response.completed",
		"incomplete": "response.incomplete",
		"failed":     "response.failed",
		"cancelled":  "response.cancelled",
		"canceled":   "response.cancelled",
		"":           "response.completed", // unknown/empty defaults to completed
	}
	for status, wantType := range cases {
		assert.Equalf(t, wantType, responsesTerminalEventTypeForStatus(status),
			"status %q should map to %q", status, wantType)
	}
}
