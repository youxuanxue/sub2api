//go:build unit

package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// buildNormalizeGrowthPayload reproduces the shape that makes the final
// normalization grow the payload after the reductions already fit.
//
// The leading assistant turn carries call-0 with no intervening user entry, so
// the first normalization (both translators run one before truncating) folds it
// into the priming assistant block. Turn dropping later removes call-0's paired
// result while that merged call stays in priming, so the second normalization
// synthesizes an error fallback result for the now-orphaned call and adds bytes.
//
// pairs/fill size the droppable history; curFill tunes the final size into the
// narrow window where the synthesized fallback crosses maxPayloadBytes.
func buildNormalizeGrowthPayload(pairs, fill, curFill int) *KiroPayload {
	payload := &KiroPayload{}
	payload.ConversationState.History = []KiroHistoryMessage{
		{UserInputMessage: &KiroUserInputMessage{Content: "system priming", Origin: "AI_EDITOR"}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "ack"}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{
			Content:  "first call",
			ToolUses: []KiroToolUse{{ToolUseID: "call-0", Name: "fs_read", Input: map[string]interface{}{"p": "/a"}}},
		}},
		{UserInputMessage: &KiroUserInputMessage{
			Origin: "AI_EDITOR",
			UserInputMessageContext: &UserInputMessageContext{ToolResults: []KiroToolResult{
				{ToolUseID: "call-0", Status: "success", Content: []KiroResultContent{{Text: "r0"}}},
			}},
		}},
	}
	for i := 1; i <= pairs; i++ {
		id := fmt.Sprintf("c%d", i)
		payload.ConversationState.History = append(payload.ConversationState.History,
			KiroHistoryMessage{AssistantResponseMessage: &KiroAssistantResponseMessage{
				Content:  strings.Repeat("a", fill),
				ToolUses: []KiroToolUse{{ToolUseID: id, Name: "fs_read", Input: map[string]interface{}{"p": "/x"}}},
			}},
			KiroHistoryMessage{UserInputMessage: &KiroUserInputMessage{
				Origin: "AI_EDITOR",
				UserInputMessageContext: &UserInputMessageContext{ToolResults: []KiroToolResult{
					{ToolUseID: id, Status: "success", Content: []KiroResultContent{{Text: strings.Repeat("r", fill)}}},
				}},
			}},
		)
	}
	current := &payload.ConversationState.CurrentMessage.UserInputMessage
	current.Origin = "AI_EDITOR"
	current.ModelID = "CLAUDE_SONNET_4_5_20250929_V1_0"
	current.Content = strings.Repeat("u", curFill)
	return payload
}

// The measured trigger: the reductions land on 921,385 bytes and the final
// normalization adds 216, one byte over the 921,600-byte limit.
const (
	normalizeGrowthPairs   = 2465
	normalizeGrowthFill    = 60
	normalizeGrowthCurFill = 183
)

// truncateWithoutRefit is the pre-fix tail: reduce, then normalize with no size
// re-check. It proves the injected regression is real rather than hypothetical.
func truncateWithoutRefit(payload *KiroPayload, hasPriming bool) {
	if payloadByteSize(payload) <= maxPayloadBytes {
		return
	}
	stripOldestHistoryImagesUntilFit(payload, hasPriming)
	if payloadByteSize(payload) <= maxPayloadBytes {
		normalizeKiroToolHistory(payload)
		return
	}
	historyAfterImages := append([]KiroHistoryMessage(nil), payload.ConversationState.History...)
	truncateHistoryTurns(payload, hasPriming)
	if payloadByteSize(payload) > maxPayloadBytes {
		clipToolResultsUntilFit(payload)
		clipNarratedToolResultsUntilFit(payload)
		dropOldestCurrentImagesUntilFit(payload)
		if payloadByteSize(payload) <= maxPayloadBytes {
			payload.ConversationState.History = historyAfterImages
			if payloadByteSize(payload) > maxPayloadBytes {
				truncateHistoryTurns(payload, hasPriming)
			}
		}
	}
	normalizeKiroToolHistory(payload)
}

// Direction 1 of the guard: without the re-check this payload is oversized, so
// the test would fail if the fix were reverted.
func TestNormalizeGrowthWithoutRefitExceedsLimit(t *testing.T) {
	payload := buildNormalizeGrowthPayload(normalizeGrowthPairs, normalizeGrowthFill, normalizeGrowthCurFill)
	normalizeKiroToolHistory(payload)
	require.Greater(t, payloadByteSize(payload), maxPayloadBytes, "fixture must start oversized")

	truncateWithoutRefit(payload, true)

	require.Greater(t, payloadByteSize(payload), maxPayloadBytes,
		"pre-fix tail must leave the payload oversized; the growth window moved")
	var tooLarge *PayloadTooLargeError
	err := ValidatePayloadSize(payload)
	require.ErrorAs(t, err, &tooLarge)
	require.Equal(t, 921601, tooLarge.Size, "measured post-normalize size")
	require.Equal(t, maxPayloadBytes, tooLarge.Limit)
}

// Direction 2: the shipped truncator fits the same payload.
func TestNormalizeGrowthRefitFitsPayload(t *testing.T) {
	payload := buildNormalizeGrowthPayload(normalizeGrowthPairs, normalizeGrowthFill, normalizeGrowthCurFill)
	normalizeKiroToolHistory(payload)

	truncatePayloadToLimit(payload, true)

	require.LessOrEqual(t, payloadByteSize(payload), maxPayloadBytes)
	require.NoError(t, ValidatePayloadSize(payload))
	require.Equal(t, strings.Repeat("u", normalizeGrowthCurFill),
		payload.ConversationState.CurrentMessage.UserInputMessage.Content,
		"current user prose must survive untouched")
	require.Equal(t, "system priming",
		payload.ConversationState.History[0].UserInputMessage.Content,
		"system priming must survive")
	require.Equal(t, payloadByteSize(payload), payload.SizeDiagnostics.After.TotalBytes,
		"diagnostics must report the post-refit size")
}

// Sweeping the window guards against a fixture that only works at one size.
func TestNormalizeGrowthRefitFitsAcrossWindow(t *testing.T) {
	for curFill := normalizeGrowthCurFill; curFill <= normalizeGrowthCurFill+40; curFill += 8 {
		payload := buildNormalizeGrowthPayload(normalizeGrowthPairs, normalizeGrowthFill, curFill)
		normalizeKiroToolHistory(payload)

		bare := buildNormalizeGrowthPayload(normalizeGrowthPairs, normalizeGrowthFill, curFill)
		normalizeKiroToolHistory(bare)
		truncateWithoutRefit(bare, true)
		require.Greater(t, payloadByteSize(bare), maxPayloadBytes,
			"curFill=%d must be inside the growth window", curFill)

		truncatePayloadToLimit(payload, true)
		require.NoError(t, ValidatePayloadSize(payload), "curFill=%d", curFill)
		require.Equal(t, strings.Repeat("u", curFill),
			payload.ConversationState.CurrentMessage.UserInputMessage.Content, "curFill=%d", curFill)
	}
}

// Idempotence: a payload the truncator already fitted is unchanged on a rerun.
func TestRefitIsIdempotent(t *testing.T) {
	payload := buildNormalizeGrowthPayload(normalizeGrowthPairs, normalizeGrowthFill, normalizeGrowthCurFill)
	normalizeKiroToolHistory(payload)
	truncatePayloadToLimit(payload, true)
	first, err := json.Marshal(payload)
	require.NoError(t, err)

	truncatePayloadToLimit(payload, true)
	second, err := json.Marshal(payload)
	require.NoError(t, err)

	require.Equal(t, string(first), string(second), "rerunning the truncator must not change the wire bytes")
}

// The contract's hard guarantee: a fitting payload stays byte-identical.
func TestRefitLeavesFittingPayloadByteIdentical(t *testing.T) {
	cases := map[string]func() *KiroPayload{
		"plain prose": func() *KiroPayload {
			payload := &KiroPayload{}
			payload.ConversationState.CurrentMessage.UserInputMessage.Content = "hello"
			return payload
		},
		"orphan tool result": func() *KiroPayload {
			// Normalization would rewrite this, but a fitting payload must not
			// reach the truncator's normalization at all.
			payload := &KiroPayload{}
			payload.ConversationState.History = []KiroHistoryMessage{
				{UserInputMessage: &KiroUserInputMessage{Content: "system priming", Origin: "AI_EDITOR"}},
				{AssistantResponseMessage: &KiroAssistantResponseMessage{
					Content:  "call",
					ToolUses: []KiroToolUse{{ToolUseID: "orphan", Name: "fs_read", Input: map[string]interface{}{"p": "/a"}}},
				}},
			}
			current := &payload.ConversationState.CurrentMessage.UserInputMessage
			current.Content = "no result supplied"
			current.Origin = "AI_EDITOR"
			return payload
		},
		"just under the limit": func() *KiroPayload {
			payload := &KiroPayload{}
			current := &payload.ConversationState.CurrentMessage.UserInputMessage
			current.Origin = "AI_EDITOR"
			current.Content = strings.Repeat("x", maxPayloadBytes-200)
			return payload
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			payload := build()
			require.LessOrEqual(t, payloadByteSize(payload), maxPayloadBytes, "fixture must already fit")
			before, err := json.Marshal(payload)
			require.NoError(t, err)

			truncatePayloadToLimit(payload, true)

			after, err := json.Marshal(payload)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "fitting payloads must stay byte-identical")
			require.Nil(t, payload.SizeDiagnostics)
		})
	}
}

// Convergence on the real trigger: dropping history must reach a fitting
// payload, and the refit loop must not keep churning once it does. Asserting
// the post-state is stable under a rerun is the observable form of "bounded".
func TestRefitConvergesOnGrowthTrigger(t *testing.T) {
	payload := buildNormalizeGrowthPayload(normalizeGrowthPairs, normalizeGrowthFill, normalizeGrowthCurFill)
	normalizeKiroToolHistory(payload)
	entriesBefore := len(payload.ConversationState.History)

	done := make(chan struct{})
	go func() {
		defer close(done)
		truncatePayloadToLimit(payload, true)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("truncatePayloadToLimit did not terminate on the growth trigger")
	}

	require.NoError(t, ValidatePayloadSize(payload))
	require.Less(t, len(payload.ConversationState.History), entriesBefore,
		"refit must shrink history to absorb the normalization growth")
	// Each round strictly shrinks the entry count, so a fitting result is final.
	settled, err := json.Marshal(payload)
	require.NoError(t, err)
	truncatePayloadToLimit(payload, true)
	again, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Equal(t, string(settled), string(again), "refit must settle rather than churn")
}

// Convergence when it cannot win: normalization keeps adding bytes because every
// retained tool call is orphaned, and the current message alone is oversized so
// no amount of history dropping can fit it. The truncator must still return, and
// must not shred history for nothing.
func TestRefitTerminatesWhenNormalizeKeepsGrowing(t *testing.T) {
	payload := &KiroPayload{}
	payload.ConversationState.History = []KiroHistoryMessage{
		{UserInputMessage: &KiroUserInputMessage{Content: "system priming", Origin: "AI_EDITOR"}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "ack"}},
	}
	for i := 0; i < 400; i++ {
		payload.ConversationState.History = append(payload.ConversationState.History,
			KiroHistoryMessage{UserInputMessage: &KiroUserInputMessage{
				Content: strings.Repeat("q", 100), Origin: "AI_EDITOR",
			}},
			KiroHistoryMessage{AssistantResponseMessage: &KiroAssistantResponseMessage{
				Content: strings.Repeat("a", 100),
				ToolUses: []KiroToolUse{
					{ToolUseID: fmt.Sprintf("never-answered-%d", i), Name: "fs_read", Input: map[string]interface{}{"p": "/x"}},
				},
			}},
		)
	}
	// Prose above the limit: unshrinkable by policy, so the loop cannot win.
	current := &payload.ConversationState.CurrentMessage.UserInputMessage
	current.Origin = "AI_EDITOR"
	current.ModelID = "CLAUDE_SONNET_4_5_20250929_V1_0"
	current.Content = strings.Repeat("u", maxPayloadBytes+5000)
	prose := current.Content

	done := make(chan struct{})
	go func() {
		defer close(done)
		truncatePayloadToLimit(payload, true)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("truncatePayloadToLimit did not terminate")
	}

	// Still oversized, so the caller owns the local 413.
	var tooLarge *PayloadTooLargeError
	require.ErrorAs(t, ValidatePayloadSize(payload), &tooLarge)
	require.Equal(t, prose, current.Content, "user prose must never be shortened to fit")
	require.Equal(t, prose, payload.ConversationState.CurrentMessage.UserInputMessage.Content,
		"an unfittable payload must not have the elision note folded into user prose")
}

// The approved policy forbids touching tool definitions on any path.
func TestRefitNeverAltersToolDefinitions(t *testing.T) {
	payload := buildNormalizeGrowthPayload(normalizeGrowthPairs, normalizeGrowthFill, normalizeGrowthCurFill)
	tools := []KiroToolWrapper{{}}
	tools[0].ToolSpecification.Name = "fs_read"
	tools[0].ToolSpecification.Description = strings.Repeat("d", 2048)
	tools[0].ToolSpecification.InputSchema = InputSchema{JSON: map[string]interface{}{"type": "object"}}
	current := &payload.ConversationState.CurrentMessage.UserInputMessage
	if current.UserInputMessageContext == nil {
		current.UserInputMessageContext = &UserInputMessageContext{}
	}
	current.UserInputMessageContext.Tools = tools
	normalizeKiroToolHistory(payload)
	expected, err := json.Marshal(payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools)
	require.NoError(t, err)

	truncatePayloadToLimit(payload, true)

	actual, err := json.Marshal(payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools)
	require.NoError(t, err)
	require.Equal(t, string(expected), string(actual), "tool definitions must not be rewritten")
}
