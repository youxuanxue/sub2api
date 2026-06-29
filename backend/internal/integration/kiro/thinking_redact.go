package kiro

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// RedactedThinkingData returns an opaque placeholder for a redacted_thinking block.
// Kiro upstream reasoning is not Anthropic-signed; emitting plaintext thinking_delta
// leaks into Claude Code terminals. OAuth parity uses redacted_thinking instead.
func RedactedThinkingData(thinking string) string {
	if strings.TrimSpace(thinking) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(thinking))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// ExtractThinkingFromContent splits inline <thinking>...</thinking> tags that some
// Kiro models embed in assistantResponseEvent text from the visible answer text.
func ExtractThinkingFromContent(content string) (visible string, thinking string) {
	result := content
	var reasoning strings.Builder

	for {
		start := strings.Index(result, "<thinking>")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "</thinking>")
		if end == -1 {
			break
		}
		end += start
		_, _ = reasoning.WriteString(result[start+len("<thinking>") : end])
		result = result[:start] + result[end+len("</thinking>"):]
	}

	return strings.TrimSpace(result), strings.TrimSpace(reasoning.String())
}
