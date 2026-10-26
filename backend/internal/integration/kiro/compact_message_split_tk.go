package kiro

import "strings"

// CompactMessageSplitReport is size/count metadata for the gated compact
// message normalization. It never contains request text.
type CompactMessageSplitReport struct {
	Candidate    bool   `json:"candidate"`
	Applied      bool   `json:"applied"`
	SkipReason   string `json:"skip_reason,omitempty"`
	MessageIndex int    `json:"message_index,omitempty"`
	BlocksBefore int    `json:"blocks_before,omitempty"`
	BlocksAfter  int    `json:"blocks_after,omitempty"`
	TextBytes    int    `json:"text_bytes,omitempty"`
}

const compactMessageMinTextBytes = 256 * 1024

// SplitClaudeCompactUserMessage preserves block boundaries in the large
// summary message emitted by Claude Code compact/autocompact. Each text block
// becomes a separate user turn, allowing the existing Kiro history truncator
// to remove complete old blocks and retain the newest block. It is deliberately
// conservative: mixed content, assistant turns, a single block, and small
// messages are left untouched.
func SplitClaudeCompactUserMessage(req *ClaudeRequest) CompactMessageSplitReport {
	rep := CompactMessageSplitReport{MessageIndex: -1}
	if req == nil || len(req.Messages) == 0 {
		rep.SkipReason = "nil_or_empty_request"
		return rep
	}
	// Auto-mode classifiers also use many text blocks, but their max_tokens is
	// 64 or less and they must stay on the existing structured-transcript path.
	if req.MaxTokens <= 64 {
		rep.SkipReason = "classifier_budget"
		return rep
	}
	if len(req.ContextManagement) == 0 {
		rep.SkipReason = "missing_context_management"
		return rep
	}
	last := len(req.Messages) - 1
	if !strings.EqualFold(req.Messages[last].Role, "user") {
		rep.SkipReason = "last_message_not_user"
		return rep
	}
	for _, msg := range req.Messages {
		if strings.EqualFold(msg.Role, "assistant") {
			rep.SkipReason = "assistant_turn_present"
			return rep
		}
	}
	blocks, ok := asClaudeContentBlocks(req.Messages[last].Content)
	if !ok || len(blocks) < 2 {
		rep.SkipReason = "not_multi_block_text"
		return rep
	}
	rep.Candidate = true
	rep.MessageIndex = last
	rep.BlocksBefore = len(blocks)
	for _, block := range blocks {
		if block.Type != "text" && block.Type != "input_text" {
			rep.SkipReason = "mixed_content_blocks"
			return rep
		}
		if block.Text == "" {
			rep.SkipReason = "empty_text_block"
			return rep
		}
		rep.TextBytes += len([]byte(block.Text))
	}
	if rep.TextBytes < compactMessageMinTextBytes {
		rep.SkipReason = "message_below_minimum"
		return rep
	}

	// Keep the original block order. The final block remains the current
	// message; preceding blocks become independently droppable history turns.
	rebuilt := make([]ClaudeMessage, 0, len(req.Messages)+len(blocks)-1)
	rebuilt = append(rebuilt, req.Messages[:last]...)
	for _, block := range blocks {
		rebuilt = append(rebuilt, ClaudeMessage{Role: "user", Content: block.Text})
	}
	rebuilt = append(rebuilt, req.Messages[last+1:]...)
	req.Messages = rebuilt
	req.preserveUserTurnBoundaries = true
	rep.Applied = true
	rep.BlocksAfter = len(blocks)
	return rep
}
