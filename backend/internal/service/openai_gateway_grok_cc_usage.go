package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func applyGrokRawChatCompletionsUsage(account *Account, body []byte) []byte {
	if account == nil || account.Platform != PlatformGrok || !gjson.ValidBytes(body) {
		return body
	}
	usage := gjson.GetBytes(body, "usage")
	prompt := usage.Get("prompt_tokens")
	completion := usage.Get("completion_tokens")
	total := usage.Get("total_tokens")
	reasoning := usage.Get("completion_tokens_details.reasoning_tokens")
	if prompt.Type != gjson.Number || completion.Type != gjson.Number || total.Type != gjson.Number || reasoning.Type != gjson.Number {
		return body
	}
	if total.Int() != prompt.Int()+completion.Int()+reasoning.Int() {
		return body
	}
	// Match billing's xAI accounting while remaining idempotent across relays.
	output := xai.IncludeIndependentReasoningTokens(prompt.Int(), completion.Int(), total.Int(), reasoning.Int())
	if output == completion.Int() {
		return body
	}
	updated, err := sjson.SetBytes(body, "usage.completion_tokens", output)
	if err != nil {
		return body
	}
	return updated
}

func applyGrokRawChatCompletionsUsageSSELine(account *Account, line string) string {
	if account == nil || account.Platform != PlatformGrok {
		return line
	}
	payload, ok := extractOpenAISSEDataLine(line)
	if !ok {
		return line
	}
	updated := applyGrokRawChatCompletionsUsage(account, []byte(payload))
	if string(updated) == payload {
		return line
	}
	return line[:len(line)-len(payload)] + string(updated)
}
