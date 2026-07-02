package service

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// estimateAnthropicCountTokensInput returns a conservative local estimate for
// Anthropic-shaped POST /v1/messages/count_tokens bodies when upstream does not
// expose a native counter (e.g. Antigravity). Mirrors the Gemini compat estimator
// heuristic: ~4 runes/token for ASCII-heavy text, ~1 rune/token for CJK-heavy text.
func estimateAnthropicCountTokensInput(body []byte) int {
	if len(body) == 0 {
		return 0
	}

	total := 0

	system := gjson.GetBytes(body, "system")
	switch {
	case system.IsArray():
		system.ForEach(func(_, block gjson.Result) bool {
			total += estimateTokensForAnthropicText(block.Get("text").String())
			return true
		})
	case system.Type == gjson.String:
		total += estimateTokensForAnthropicText(system.String())
	}

	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		switch {
		case content.Type == gjson.String:
			total += estimateTokensForAnthropicText(content.String())
		case content.IsArray():
			content.ForEach(func(_, block gjson.Result) bool {
				switch block.Get("type").String() {
				case "text":
					total += estimateTokensForAnthropicText(block.Get("text").String())
				case "tool_use":
					total += estimateTokensForAnthropicText(block.Get("name").String())
					total += estimateTokensForAnthropicText(block.Get("input").Raw)
				case "tool_result":
					total += estimateTokensForAnthropicText(block.Get("content").String())
				}
				return true
			})
		}
		return true
	})

	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		total += estimateTokensForAnthropicText(tool.Get("name").String())
		total += estimateTokensForAnthropicText(tool.Get("description").String())
		total += estimateTokensForAnthropicText(tool.Get("input_schema").Raw)
		return true
	})

	if total < 1 && json.Valid(body) {
		// Non-empty JSON body with no countable text still gets a floor of 1 so
		// clients never see a zero-token estimate for a real request.
		total = 1
	}
	if total < 0 {
		return 0
	}
	return total
}

func estimateTokensForAnthropicText(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	runes := []rune(s)
	if len(runes) == 0 {
		return 0
	}
	ascii := 0
	for _, r := range runes {
		if r <= 0x7f {
			ascii++
		}
	}
	asciiRatio := float64(ascii) / float64(len(runes))
	if asciiRatio >= 0.8 {
		return (len(runes) + 3) / 4
	}
	return len(runes)
}
