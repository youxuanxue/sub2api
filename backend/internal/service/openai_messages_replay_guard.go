package service

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

const (
	openAICompatAnthropicReplayMaxTailMessages = 12
	openAICompatOAuthReplayAnchorMessages      = 2
)

func applyAnthropicCompatFullReplayGuard(req *apicompat.AnthropicRequest) bool {
	if req == nil || len(req.Messages) <= openAICompatAnthropicReplayMaxTailMessages {
		return false
	}

	start := len(req.Messages) - openAICompatAnthropicReplayMaxTailMessages
	start = expandAnthropicCompatTrimBoundary(req.Messages, start)
	if start <= 0 {
		return false
	}

	req.Messages = append([]apicompat.AnthropicMessage(nil), req.Messages[start:]...)
	return true
}

func applyOpenAICompatOAuthMessagesCompaction(req *apicompat.AnthropicRequest) bool {
	if req == nil || len(req.Messages) <= openAICompatOAuthReplayAnchorMessages+openAICompatAnthropicReplayMaxTailMessages {
		return false
	}

	prefixEnd := openAICompatOAuthReplayAnchorMessages
	if prefixEnd > len(req.Messages) {
		prefixEnd = len(req.Messages)
	}
	prefixEnd = expandAnthropicCompatPrefixBoundary(req.Messages, prefixEnd)

	tailStart := len(req.Messages) - openAICompatAnthropicReplayMaxTailMessages
	tailStart = expandAnthropicCompatTrimBoundary(req.Messages, tailStart)
	if tailStart <= prefixEnd {
		return false
	}

	trimmed := make([]apicompat.AnthropicMessage, 0, prefixEnd+len(req.Messages)-tailStart)
	trimmed = append(trimmed, req.Messages[:prefixEnd]...)
	trimmed = append(trimmed, req.Messages[tailStart:]...)
	if len(trimmed) >= len(req.Messages) {
		return false
	}

	req.Messages = trimmed
	return true
}

func expandAnthropicCompatTrimBoundary(messages []apicompat.AnthropicMessage, start int) int {
	if start <= 0 || start >= len(messages) {
		return start
	}

	toolUseIndex := make(map[string]int)
	toolResultIndex := make(map[string]int)
	for i, msg := range messages {
		uses, results := anthropicCompatMessageToolIDs(msg)
		for _, id := range uses {
			if _, exists := toolUseIndex[id]; !exists {
				toolUseIndex[id] = i
			}
		}
		for _, id := range results {
			if _, exists := toolResultIndex[id]; !exists {
				toolResultIndex[id] = i
			}
		}
	}

	for {
		next := start
		for i := start; i < len(messages); i++ {
			uses, results := anthropicCompatMessageToolIDs(messages[i])
			for _, id := range results {
				if useIdx, ok := toolUseIndex[id]; ok && useIdx < next {
					next = useIdx
				}
			}
			for _, id := range uses {
				if resultIdx, ok := toolResultIndex[id]; ok && resultIdx < next {
					next = resultIdx
				}
			}
		}
		if next == start {
			return start
		}
		start = next
	}
}

func expandAnthropicCompatPrefixBoundary(messages []apicompat.AnthropicMessage, end int) int {
	if end <= 0 {
		return end
	}
	if end >= len(messages) {
		return len(messages)
	}

	toolUseIndex := make(map[string]int)
	toolResultIndex := make(map[string]int)
	for i, msg := range messages {
		uses, results := anthropicCompatMessageToolIDs(msg)
		for _, id := range uses {
			if _, exists := toolUseIndex[id]; !exists {
				toolUseIndex[id] = i
			}
		}
		for _, id := range results {
			if _, exists := toolResultIndex[id]; !exists {
				toolResultIndex[id] = i
			}
		}
	}

	for {
		next := end
		for i := 0; i < end; i++ {
			uses, results := anthropicCompatMessageToolIDs(messages[i])
			for _, id := range uses {
				if resultIdx, ok := toolResultIndex[id]; ok && resultIdx+1 > next {
					next = resultIdx + 1
				}
			}
			for _, id := range results {
				if useIdx, ok := toolUseIndex[id]; ok && useIdx+1 > next {
					next = useIdx + 1
				}
			}
		}
		if next == end {
			return end
		}
		if next >= len(messages) {
			return len(messages)
		}
		end = next
	}
}

func anthropicCompatMessageToolIDs(msg apicompat.AnthropicMessage) ([]string, []string) {
	var blocks []apicompat.AnthropicContentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil, nil
	}

	uses := make([]string, 0, 1)
	results := make([]string, 0, 1)
	for _, block := range blocks {
		switch block.Type {
		case "tool_use":
			if block.ID != "" {
				uses = append(uses, block.ID)
			}
		case "tool_result":
			if block.ToolUseID != "" {
				results = append(results, block.ToolUseID)
			}
		}
	}
	return uses, results
}
