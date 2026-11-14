package service

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyNewAPIAnthropicThinkingTemperature pins temperature to 1 when the
// outbound Claude request will run with thinking enabled. new-api's Claude
// adaptor copies client temperature and only rewrites it for "-thinking" /
// a few Opus 4.x effort suffixes; otherwise Anthropic returns
// "temperature may only be set to 1 when thinking is enabled".
//
// Policy (aligned with new-api / LiteLLM / CLIProxyAPI): normalize outbound
// sampling instead of failing the client. Opus 5.5 defaults to adaptive
// thinking, so any non-default temperature on that model is pinned even
// without an explicit thinking field.
func applyNewAPIAnthropicThinkingTemperature(account *Account, model string, body []byte) []byte {
	if len(body) == 0 || account == nil || account.ChannelType != newapiconstant.ChannelTypeAnthropic {
		return body
	}
	if !newAPIClaudeRequestEnablesThinking(model, body) {
		return body
	}
	temperature := gjson.GetBytes(body, "temperature")
	if !temperature.Exists() || (temperature.Type == gjson.Number && temperature.Float() == 1) {
		return body
	}
	next, err := sjson.SetBytes(body, "temperature", 1.0)
	if err != nil {
		return body
	}
	return next
}

func newAPIClaudeRequestEnablesThinking(model string, body []byte) bool {
	model = strings.TrimSpace(model)
	if model == "" || !strings.Contains(strings.ToLower(model), "claude") {
		return false
	}
	// Opus 5.5 adaptive thinking is on by default at Anthropic; non-1
	// temperature is rejected even when the client omits thinking fields.
	if claude.IsOpus55(model) {
		return true
	}
	if strings.HasSuffix(model, "-thinking") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())) {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String())) {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String())) {
	case "enabled", "adaptive", "between_tools":
		return true
	}
	return gjson.GetBytes(body, "reasoning.max_tokens").Int() > 0
}
