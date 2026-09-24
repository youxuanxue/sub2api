package apicompat

import "encoding/json"

// Keep decoded content through merge -> tool pairing -> merge. Raw content stays
// authoritative until a step changes it, preserving untouched singleton bytes.
// This scratch state belongs to one conversion; it never caches account facts.
type anthropicHistoryMessage struct {
	AnthropicMessage
	blocks  []AnthropicContentBlock
	decoded bool
	changed bool
}

func (m *anthropicHistoryMessage) contentBlocks() []AnthropicContentBlock {
	if !m.decoded {
		m.blocks = parseContentBlocks(m.Content)
		m.decoded = true
	}
	return m.blocks
}

func anthropicHistoryFromBlocks(role string, blocks []AnthropicContentBlock) anthropicHistoryMessage {
	return anthropicHistoryMessage{AnthropicMessage: AnthropicMessage{Role: role}, blocks: blocks, decoded: true, changed: true}
}

func newAnthropicHistory(messages []AnthropicMessage) []anthropicHistoryMessage {
	history := make([]anthropicHistoryMessage, len(messages))
	for i, message := range messages {
		history[i].AnthropicMessage = message
	}
	return history
}

func materializeAnthropicHistory(history []anthropicHistoryMessage) []AnthropicMessage {
	messages := make([]AnthropicMessage, len(history))
	for i, message := range history {
		messages[i] = message.AnthropicMessage
		if message.changed {
			messages[i].Content, _ = json.Marshal(message.blocks)
		}
	}
	return messages
}

func normalizeAnthropicHistory(messages []AnthropicMessage) []AnthropicMessage {
	history := mergeAnthropicHistory(newAnthropicHistory(messages))
	history = normalizeAnthropicToolPairing(history)
	return materializeAnthropicHistory(mergeAnthropicHistory(history))
}

func mergeAnthropicHistory(messages []anthropicHistoryMessage) []anthropicHistoryMessage {
	if len(messages) <= 1 {
		return messages
	}
	merged := make([]anthropicHistoryMessage, 0, len(messages))
	for start := 0; start < len(messages); {
		end := start + 1
		for end < len(messages) && messages[end].Role == messages[start].Role {
			end++
		}
		msg := messages[start]
		if end > start+1 {
			blocks := msg.contentBlocks()
			for i := start + 1; i < end; i++ {
				blocks = append(blocks, messages[i].contentBlocks()...)
			}
			msg = anthropicHistoryFromBlocks(msg.Role, blocks)
		}
		merged = append(merged, msg)
		start = end
	}
	return merged
}

// normalizeAnthropicToolPairing rebuilds the message sequence so it satisfies
// Anthropic's tool_use/tool_result invariants, which the naive item-by-item
// conversion violates whenever the Responses history interleaves anything
// between a function_call and its function_call_output:
//
//   - every tool_result block must have a matching tool_use in the immediately
//     preceding assistant message ("tool_result ... must have a corresponding
//     tool_use block in the previous message");
//   - every tool_use block must be answered by a tool_result in the immediately
//     following user message (Anthropic rejects unanswered tool_use ids);
//   - user/assistant turns must alternate.
//
// codex (Responses, store:false) re-sends the whole history each turn and
// frequently injects items between a call and its output — a developer/approval
// notice, or a sibling parallel call whose output never arrived. The unrepaired
// converter emits each function_call as its own assistant message and each
// output as its own user message, so any such interleaving breaks
// tool_use↔tool_result adjacency and yields an upstream 400.
//
// The repair indexes every tool_result by its tool_use id, then for each
// assistant message carrying tool_use blocks keeps only the answered ones
// (dropping unanswered/dangling calls — and the assistant message entirely if it
// has no other content) and emits the matching tool_result blocks, in call
// order, as the very next user message. Standalone tool_result blocks are
// dropped from their original position (re-emitted adjacent to their call);
// orphan tool_results with no announcing tool_use are dropped. Non-tool content
// passes through in place. This mirrors normalizeChatMessages on the
// Responses→Chat path.
func normalizeAnthropicToolPairing(messages []anthropicHistoryMessage) []anthropicHistoryMessage {
	// Index every tool_result block by its tool_use id (last wins on dup).
	results := make(map[string]AnthropicContentBlock)
	for i := range messages {
		m := &messages[i]
		if m.Role != "user" {
			continue
		}
		for _, b := range m.contentBlocks() {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				results[b.ToolUseID] = b
			}
		}
	}

	out := make([]anthropicHistoryMessage, 0, len(messages))
	for i := range messages {
		m := &messages[i]
		blocks := m.contentBlocks()
		switch m.Role {
		case "assistant":
			var toolUses, others []AnthropicContentBlock
			for _, b := range blocks {
				if b.Type == "tool_use" {
					toolUses = append(toolUses, b)
				} else {
					others = append(others, b)
				}
			}
			if len(toolUses) == 0 {
				out = append(out, *m)
				continue
			}
			kept := make([]AnthropicContentBlock, 0, len(toolUses))
			for _, tu := range toolUses {
				if _, ok := results[tu.ID]; ok {
					kept = append(kept, tu)
				}
			}
			if len(kept) == 0 {
				// No answered calls: keep any non-tool content, else drop.
				if len(others) > 0 {
					out = append(out, anthropicHistoryFromBlocks("assistant", others))
				}
				continue
			}
			asstBlocks := make([]AnthropicContentBlock, 0, len(others)+len(kept))
			asstBlocks = append(asstBlocks, others...)
			asstBlocks = append(asstBlocks, kept...)
			out = append(out, anthropicHistoryFromBlocks("assistant", asstBlocks))

			resBlocks := make([]AnthropicContentBlock, 0, len(kept))
			for _, tu := range kept {
				resBlocks = append(resBlocks, results[tu.ID])
			}
			out = append(out, anthropicHistoryFromBlocks("user", resBlocks))

		case "user":
			var nonResult []AnthropicContentBlock
			hasResult := false
			for _, b := range blocks {
				if b.Type == "tool_result" {
					hasResult = true
					continue
				}
				nonResult = append(nonResult, b)
			}
			if !hasResult {
				out = append(out, *m)
				continue
			}
			// The tool_result blocks are re-emitted next to their call; keep any
			// other content of this user turn in place, drop it if there is none.
			if len(nonResult) > 0 {
				out = append(out, anthropicHistoryFromBlocks("user", nonResult))
			}

		default:
			out = append(out, *m)
		}
	}
	return out
}
