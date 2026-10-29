package service

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyNewAPIAnthropicThinkingTemperature pins temperature to 1 before the
// new-api Claude adaptor turns reasoning_effort / reasoning.max_tokens /
// "-thinking" into thinking.type=enabled. That adaptor copies the client
// temperature and only rewrites it for the "-thinking" suffix and a few Opus
// 4.x effort suffixes, so opus-5.5 requests fail upstream with
// "temperature may only be set to 1 when thinking is enabled".
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
	if strings.HasSuffix(model, "-thinking") {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())) {
	case "low", "medium", "high":
		return true
	}
	return gjson.GetBytes(body, "reasoning.max_tokens").Int() > 0
}
