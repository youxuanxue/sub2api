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
	var redactor InlineThinkingRedactor
	visible, thinking = redactor.Push(content)
	tailVisible, tailThinking := redactor.Flush()
	if tailVisible != "" {
		if visible == "" {
			visible = tailVisible
		} else {
			visible += tailVisible
		}
	}
	if tailThinking != "" {
		if thinking == "" {
			thinking = tailThinking
		} else {
			thinking += tailThinking
		}
	}
	return strings.TrimSpace(visible), strings.TrimSpace(thinking)
}

// InlineThinkingRedactor removes inline <thinking>...</thinking> spans from a
// stream of assistant text chunks. Kiro may split the tags across chunks, so a
// stateless string replace leaks partial tags and reasoning text.
type InlineThinkingRedactor struct {
	pending    string
	inThinking bool
}

// Push consumes one upstream assistant text chunk and returns visible text plus
// extracted reasoning. It may hold a short suffix in pending when that suffix
// could be the start of a split thinking tag.
func (r *InlineThinkingRedactor) Push(content string) (visible string, thinking string) {
	r.pending += content
	var out strings.Builder
	var reasoning strings.Builder

	for r.pending != "" {
		if r.inThinking {
			end := strings.Index(r.pending, "</thinking>")
			if end == -1 {
				keep := longestTagPrefixSuffix(r.pending, "</thinking>")
				if len(r.pending) > keep {
					_, _ = reasoning.WriteString(r.pending[:len(r.pending)-keep])
					r.pending = r.pending[len(r.pending)-keep:]
				}
				break
			}
			_, _ = reasoning.WriteString(r.pending[:end])
			r.pending = r.pending[end+len("</thinking>"):]
			r.inThinking = false
			continue
		}

		start := strings.Index(r.pending, "<thinking>")
		if start == -1 {
			keep := longestTagPrefixSuffix(r.pending, "<thinking>")
			if len(r.pending) > keep {
				_, _ = out.WriteString(r.pending[:len(r.pending)-keep])
				r.pending = r.pending[len(r.pending)-keep:]
			}
			break
		}
		_, _ = out.WriteString(r.pending[:start])
		r.pending = r.pending[start+len("<thinking>"):]
		r.inThinking = true
	}

	return out.String(), reasoning.String()
}

// Flush releases any text held for split-tag detection. If upstream ends while a
// thinking block is still open, the buffered tail is treated as reasoning so it
// remains redacted rather than leaking into visible text.
func (r *InlineThinkingRedactor) Flush() (visible string, thinking string) {
	if r.pending == "" {
		return "", ""
	}
	if r.inThinking {
		thinking = r.pending
	} else if longestTagPrefixSuffix(r.pending, "<thinking>") == len(r.pending) ||
		longestTagPrefixSuffix(r.pending, "</thinking>") == len(r.pending) {
		thinking = r.pending
	} else {
		visible = r.pending
	}
	r.pending = ""
	r.inThinking = false
	return visible, thinking
}

func longestTagPrefixSuffix(s, tag string) int {
	max := len(tag) - 1
	if len(s) < max {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}
