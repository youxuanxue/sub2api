package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// extractGeminiReasoningEffortFromBody reads only an explicit level from the
// forwarded Gemini request. A budget or an omitted level does not define a
// billing level, even when the upstream model enables thinking by default.
// geminiChatForwardsReasoningEffort 决定 Chat Completions 的 reasoning_effort
// 是否写成上游 thinkingConfig。裸 gemini-3.1-pro 不接收这个档位，转发后会计入
// 并未实际上游生效的 reasoning 倍率。Flash 与其余 Gemini 3 仍转发。
// Claude 请求体继续走 applyGeminiCompatThinkingConfig，不受此门禁影响。
func geminiChatForwardsReasoningEffort(model string) bool {
	name := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	return name != "gemini-3.1-pro"
}

func extractGeminiReasoningEffortFromBody(body []byte) *string {
	config := gjson.GetBytes(body, "generationConfig")
	if !config.Exists() {
		config = gjson.GetBytes(body, "generation_config")
	}
	thinking := config.Get("thinkingConfig")
	if !thinking.Exists() {
		thinking = config.Get("thinking_config")
	}
	level := thinking.Get("thinkingLevel")
	if !level.Exists() {
		level = thinking.Get("thinking_level")
	}
	effort := strings.ToLower(strings.TrimSpace(level.String()))
	switch effort {
	case "minimal", "low", "medium", "high":
		return &effort
	default:
		return nil
	}
}
