package anthropicpolicy

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Capabilities describes the resolved model on one endpoint protocol. Overrides
// must come from endpoint evidence, never from the public alias or account name.
type Capabilities struct {
	AlwaysThinking          bool `json:"always_thinking"`
	AdaptiveOnlyThinking    bool `json:"adaptive_only_thinking"`
	ForcedToolsWithThinking bool `json:"forced_tools_with_thinking"`
}

func MessagesCapabilities(model string) Capabilities {
	model = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "anthropic.")
	fable := model == "claude-fable-5" || strings.HasPrefix(model, "claude-fable-5-") || model == "claude-fable-5[1m]"
	return Capabilities{AlwaysThinking: fable, AdaptiveOnlyThinking: fable}
}

const ThinkingPreferred = "thinking_priority_compatibility"

// Normalize preserves thinking and every tool/history/cache field. Native
// thinking modes are adapted only when model capability requires it; none is
// never interpreted as a forced tool choice.
func Normalize(body []byte, messages bool, capabilities Capabilities) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return body, false
	}
	thinking := gjson.GetBytes(body, "thinking.type").String()
	next, changed := body, false
	if (capabilities.AlwaysThinking && thinking == "disabled") || (capabilities.AdaptiveOnlyThinking && thinking == "enabled") {
		var err error
		if thinking == "disabled" {
			next, err = sjson.DeleteBytes(next, "thinking")
		} else {
			next, err = sjson.SetBytes(next, "thinking.type", "adaptive")
			if err == nil {
				next, err = sjson.DeleteBytes(next, "thinking.budget_tokens")
			}
		}
		if err != nil {
			return body, false
		}
		changed = true
	}
	enabled := capabilities.AlwaysThinking || thinking == "enabled" || thinking == "adaptive"
	if !messages {
		for _, field := range []string{"reasoning_effort", "reasoning.effort"} {
			switch gjson.GetBytes(body, field).String() {
			case "minimal", "low", "medium", "high", "xhigh", "max":
				enabled = true
			}
		}
	}
	if !enabled || capabilities.ForcedToolsWithThinking {
		return next, changed
	}
	choice := gjson.GetBytes(body, "tool_choice")
	forced := choice.Type == gjson.String && choice.String() == "required"
	if choice.IsObject() {
		kind := choice.Get("type").String()
		forced = (messages && (kind == "any" || kind == "tool")) || (!messages && kind == "function")
	}
	if !forced {
		return next, changed
	}
	var replacement any = "auto"
	if messages {
		object := map[string]any{"type": "auto"}
		if parallel := choice.Get("disable_parallel_tool_use"); parallel.Type == gjson.True || parallel.Type == gjson.False {
			object["disable_parallel_tool_use"] = parallel.Bool()
		}
		replacement = object
	}
	out, err := sjson.SetBytes(next, "tool_choice", replacement)
	if err != nil {
		return body, false
	}
	return out, true
}
