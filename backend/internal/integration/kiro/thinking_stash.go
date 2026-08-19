package kiro

import "strings"

const kiroReasoningEventPlaceholder = "..."

func isKiroReasoningPlaceholder(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed == "" || trimmed == kiroReasoningEventPlaceholder
}

// ResolveStashThinking assembles QA/traj thinking plaintext from gateway
// accumulators. Client thinking.display must not influence this path.
func ResolveStashThinking(rawAssistant, turnThinking, signature string) string {
	if extracted := strings.TrimSpace(nonPlaceholderThinking(turnThinking)); extracted != "" {
		return extracted
	}
	if _, tagged := ExtractThinkingFromContent(rawAssistant); tagged != "" {
		return tagged
	}
	if strings.TrimSpace(signature) == "" {
		return ""
	}
	return ExtractTaglessThinking(rawAssistant)
}

func nonPlaceholderThinking(text string) string {
	text = strings.TrimSpace(text)
	if isKiroReasoningPlaceholder(text) {
		return ""
	}
	return text
}

// ExtractTaglessThinking splits reasoning from a trailing short answer segment
// in tagless Kiro assistant streams.
func ExtractTaglessThinking(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	if _, tagged := ExtractThinkingFromContent(content); tagged != "" {
		return tagged
	}
	if idx := strings.LastIndex(content, "\n\n"); idx >= 0 {
		head := strings.TrimSpace(content[:idx])
		tail := strings.TrimSpace(content[idx+2:])
		if head != "" && tail != "" &&
			len([]rune(tail)) <= 256 &&
			len([]rune(head)) > len([]rune(tail)) {
			return head
		}
	}
	return content
}
