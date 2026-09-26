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
	return normalizeValidated(body, messages, capabilities)
}

// Facts is the body-derived half of a Normalize decision. Every field is read
// from the request bytes and the inbound protocol alone, both of which are
// immutable within one request, while Capabilities supplies the per-route half.
// A caller that evaluates many routes over one body therefore derives Facts once
// and passes it to NormalizeWithFacts instead of re-walking the body per route.
type Facts struct {
	// thinking is the raw "thinking.type" value; the rewrites distinguish
	// "disabled" from "enabled", so a single enabled bool cannot replace it.
	thinking string
	// reasoningEffort already accounts for the protocol: it can only be true for
	// a non-Messages inbound, matching where Normalize reads those fields.
	reasoningEffort  bool
	toolChoiceForced bool
	// disableParallelToolUse is carried through the Messages tool_choice rewrite
	// only when the original document stated it as a boolean.
	disableParallelToolUseSet bool
	disableParallelToolUse    bool
}

// InspectValidated derives Facts from a body whose JSON validity the caller has
// already established. Deriving it from unvalidated bytes would let gjson's
// reading of a malformed document drive a rewrite, so such callers must stay on
// Normalize, which validates and fails closed.
func InspectValidated(body []byte, messages bool) Facts {
	facts := Facts{thinking: gjson.GetBytes(body, "thinking.type").String()}
	if !messages {
		for _, field := range []string{"reasoning_effort", "reasoning.effort"} {
			switch gjson.GetBytes(body, field).String() {
			case "minimal", "low", "medium", "high", "xhigh", "max":
				facts.reasoningEffort = true
			}
		}
	}
	choice := gjson.GetBytes(body, "tool_choice")
	facts.toolChoiceForced = choice.Type == gjson.String && choice.String() == "required"
	if choice.IsObject() {
		kind := choice.Get("type").String()
		facts.toolChoiceForced = (messages && (kind == "any" || kind == "tool")) || (!messages && kind == "function")
		if parallel := choice.Get("disable_parallel_tool_use"); parallel.Type == gjson.True || parallel.Type == gjson.False {
			facts.disableParallelToolUseSet = true
			facts.disableParallelToolUse = parallel.Bool()
		}
	}
	return facts
}

func normalizeValidated(body []byte, messages bool, capabilities Capabilities) ([]byte, bool) {
	return NormalizeWithFacts(body, messages, InspectValidated(body, messages), capabilities)
}

// NormalizeWithFacts is normalizeValidated with the body-derived reads already
// done. Facts must have been derived from these exact bytes and this same
// messages flag; deriving it from anything else would apply another request's
// policy to this one.
func NormalizeWithFacts(body []byte, messages bool, facts Facts, capabilities Capabilities) ([]byte, bool) {
	thinking := facts.thinking
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
	enabled := capabilities.AlwaysThinking || thinking == "enabled" || thinking == "adaptive" ||
		facts.reasoningEffort
	if !enabled || capabilities.ForcedToolsWithThinking {
		return next, changed
	}
	if !facts.toolChoiceForced {
		return next, changed
	}
	var replacement any = "auto"
	if messages {
		object := map[string]any{"type": "auto"}
		if facts.disableParallelToolUseSet {
			object["disable_parallel_tool_use"] = facts.disableParallelToolUse
		}
		replacement = object
	}
	out, err := sjson.SetBytes(next, "tool_choice", replacement)
	if err != nil {
		return body, false
	}
	return out, true
}
