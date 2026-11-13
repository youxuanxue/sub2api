package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyMoonshotK3DynamicToolsShape promotes Moonshot-style dynamic tool injection
// (system message carrying tools without usable content) into top-level tools and
// drops those messages.
//
// Official Moonshot accepts that message shape. Volc Agent Plan (china kimi-k3
// supply) rejects it with missing content. Promoting keeps Calculator-style
// admission probes working across K3 supplies without requiring clients to
// change payload shape. When the client omitted tool_choice, set "required" so
// the promoted tools are actually invoked instead of the model answering inline.
//
// Validation runs here, before promotion and before any account check, so every
// caller rejects the same shapes. A repeated function name is a 400. This path
// does not silently drop the later declaration.
func applyMoonshotK3DynamicToolsShape(model string, body []byte) ([]byte, error) {
	if len(body) == 0 || !isKimiModel(model) {
		return body, nil
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return body, nil
	}
	if err := validateMoonshotK3DynamicToolMessages(body, messages); err != nil {
		return body, err
	}

	kept := make([]any, 0, len(messages.Array()))
	promoted := make([]any, 0)
	for _, msg := range messages.Array() {
		if isMoonshotDynamicToolSystemMessage(msg) {
			for _, tool := range msg.Get("tools").Array() {
				promoted = append(promoted, tool.Value())
			}
			continue
		}
		kept = append(kept, msg.Value())
	}
	if len(promoted) == 0 {
		return body, nil
	}

	allTools := make([]any, 0)
	if existing := gjson.GetBytes(body, "tools"); existing.IsArray() {
		for _, tool := range existing.Array() {
			allTools = append(allTools, tool.Value())
		}
	}
	// Names were checked above. Distinct declarations are concatenated.
	// An empty function.name is left in place so the later blank-name check
	// still sees it on top-level tools.
	allTools = append(allTools, promoted...)

	shaped := body
	if next, err := sjson.SetBytes(shaped, "messages", kept); err == nil {
		shaped = next
	}
	if next, err := sjson.SetBytes(shaped, "tools", allTools); err == nil {
		shaped = next
	}
	choice := gjson.GetBytes(shaped, "tool_choice")
	if !choice.Exists() || choice.Type == gjson.Null {
		if next, err := sjson.SetBytes(shaped, "tool_choice", "required"); err == nil {
			shaped = next
		}
	}
	return shaped, nil
}

// validateMoonshotK3DynamicToolMessages rejects KVV dynamic-tool shapes before
// any message is rewritten. Other models never reach this function.
func validateMoonshotK3DynamicToolMessages(body []byte, messages gjson.Result) error {
	seen := make(map[string]struct{})
	if existing := gjson.GetBytes(body, "tools"); existing.IsArray() {
		for _, tool := range existing.Array() {
			if name := trimmedMoonshotFunctionName(tool); name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	for _, msg := range messages.Array() {
		tools := msg.Get("tools")
		if !tools.Exists() {
			continue
		}
		if !strings.EqualFold(msg.Get("role").String(), "system") {
			return newOpenAIInvalidParameterError("messages", "tools is only allowed on system messages")
		}
		if !moonshotDynamicToolContentIsEmpty(msg.Get("content")) {
			return newOpenAIInvalidParameterError("messages", "system message with tools cannot include content")
		}
		if !tools.IsArray() {
			return newOpenAIInvalidParameterError("tools", "tools must be an array")
		}
		for _, tool := range tools.Array() {
			if !tool.IsObject() {
				return newOpenAIInvalidParameterError("tools", "tools entries must be objects")
			}
			if err := requireMoonshotDynamicToolFields(tool); err != nil {
				return err
			}
			if name := trimmedMoonshotFunctionName(tool); name != "" {
				if _, exists := seen[name]; exists {
					return newOpenAIInvalidParameterError("tools", "function.name must be unique")
				}
				seen[name] = struct{}{}
			}
		}
	}
	return nil
}

func requireMoonshotDynamicToolFields(tool gjson.Result) error {
	typ := tool.Get("type")
	if !typ.Exists() || typ.Type == gjson.Null {
		return newOpenAIInvalidParameterError("tools", "tool type is required")
	}
	fn := tool.Get("function")
	if !fn.Exists() || fn.Type == gjson.Null || !fn.IsObject() {
		return newOpenAIInvalidParameterError("tools", "tool function is required")
	}
	name := fn.Get("name")
	if !name.Exists() || name.Type == gjson.Null {
		return newOpenAIInvalidParameterError("tools", "function.name is required")
	}
	return nil
}

// trimmedMoonshotFunctionName is the function name used for duplicate checks.
// Blank and whitespace-only names are empty so the later blank-name check still
// owns them. Chat Completions nests the name; a flat name is the fallback.
func trimmedMoonshotFunctionName(tool gjson.Result) string {
	name := tool.Get("function.name")
	if !name.Exists() || name.Type == gjson.Null {
		name = tool.Get("name")
	}
	if !name.Exists() || name.Type == gjson.Null {
		return ""
	}
	return strings.TrimSpace(name.String())
}

// moonshotDynamicToolContentIsEmpty is the content that may still carry dynamic
// tools: missing, JSON null, empty string, or whitespace only.
func moonshotDynamicToolContentIsEmpty(content gjson.Result) bool {
	if !content.Exists() || content.Type == gjson.Null {
		return true
	}
	return content.Type == gjson.String && strings.TrimSpace(content.String()) == ""
}

// isMoonshotDynamicToolSystemMessage reports a system message whose tools should
// be promoted. Callers reject illegal shapes first. Content must be absent,
// JSON null, or empty/whitespace, and tools must be a non-empty array.
func isMoonshotDynamicToolSystemMessage(msg gjson.Result) bool {
	if !strings.EqualFold(msg.Get("role").String(), "system") {
		return false
	}
	tools := msg.Get("tools")
	if !tools.Exists() || !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	return moonshotDynamicToolContentIsEmpty(msg.Get("content"))
}
