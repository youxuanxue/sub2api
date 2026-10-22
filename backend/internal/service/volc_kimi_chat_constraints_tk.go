package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeVolcKimiChatRequest owns the evidenced Volc Agent Plan Kimi K3
// thinking/tool contract while presenting a Moonshot-compatible public surface
// for kimi-k3. See docs/approved/volc-kimi-chat-compatibility.md.
func normalizeVolcKimiChatRequest(body []byte) ([]byte, error) {
	shaped, err := normalizeVolcKimiThinking(body)
	if err != nil {
		return nil, err
	}
	shaped, err = normalizeVolcKimiAllowedTools(shaped)
	if err != nil {
		return nil, err
	}
	// A single allowed function + required is equivalent to a named selection,
	// and works with this supply's default thinking (named selection returns 400).
	if gjson.GetBytes(shaped, "tool_choice.type").String() != "function" {
		return shaped, nil
	}
	if gjson.GetBytes(shaped, "function_call").Exists() || gjson.GetBytes(shaped, "functions").Exists() {
		return nil, newOpenAIInvalidParameterError("tool_choice", "cannot combine named tool_choice with legacy function fields")
	}
	shaped, err = normalizeChatToolConstraints(shaped)
	if err != nil {
		return nil, newOpenAIInvalidParameterError("tool_choice", err.Error())
	}
	return sjson.SetBytes(shaped, "tool_choice", "required")
}

func normalizeVolcKimiThinking(body []byte) ([]byte, error) {
	selected := ""
	paths := []string{"thinking", "enable_thinking", "chat_template_kwargs.thinking", "chat_template_kwargs.enable_thinking"}
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if !value.Exists() || value.Type == gjson.Null {
			continue
		}
		mode := ""
		switch value.Type {
		case gjson.True:
			mode = "enabled"
		case gjson.False:
			mode = "disabled"
		default:
			if value.IsObject() && (path == "thinking" || path == "chat_template_kwargs.thinking") {
				mode = value.Get("type").String()
			}
		}
		if mode != "enabled" && mode != "disabled" && mode != "auto" {
			return nil, newOpenAIInvalidParameterError(path, "thinking must be a boolean or an object with type enabled, disabled or auto")
		}
		if selected != "" && selected != mode {
			return nil, newOpenAIInvalidParameterError("thinking", "conflicting thinking switches")
		}
		selected = mode
	}
	shaped := body
	var err error
	if selected != "" {
		// Preserve provider options in a canonical object; scalar aliases are rebuilt.
		if !gjson.GetBytes(shaped, "thinking").IsObject() {
			shaped, err = sjson.DeleteBytes(shaped, "thinking")
			if err != nil {
				return nil, err
			}
		}
		shaped, err = sjson.SetBytes(shaped, "thinking.type", selected)
		if err != nil {
			return nil, err
		}
		for _, path := range paths[1:] {
			shaped, err = sjson.DeleteBytes(shaped, path)
			if err != nil {
				return nil, err
			}
		}
		if kwargs := gjson.GetBytes(shaped, "chat_template_kwargs"); kwargs.IsObject() && len(kwargs.Map()) == 0 {
			shaped, err = sjson.DeleteBytes(shaped, "chat_template_kwargs")
			if err != nil {
				return nil, err
			}
		}
	}

	// Moonshot clients may attach Anthropic-style budget_tokens; drop before relay.
	if gjson.GetBytes(shaped, "thinking.budget_tokens").Exists() {
		shaped, err = sjson.DeleteBytes(shaped, "thinking.budget_tokens")
		if err != nil {
			return nil, err
		}
	}

	shaped = promoteMoonshotNestedReasoningEffort(shaped)
	shaped, err = normalizeVolcKimiReasoningEffort(shaped, selected)
	return shaped, err
}

// normalizeVolcKimiReasoningEffort accepts Moonshot kimi-k3 reasoning_effort
// vocabulary (and common aliases) on the public surface. Effort alone must
// produce observable thinking: when the client did not explicitly disable
// thinking, inject thinking.type=enabled so intensity probes are not silent.
func normalizeVolcKimiReasoningEffort(body []byte, thinkingMode string) ([]byte, error) {
	if !gjson.GetBytes(body, "reasoning_effort").Exists() {
		return body, nil
	}
	effort := strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String())
	mapped := mapMoonshotK3ReasoningEffort(effort)
	if mapped == "" {
		return body, newOpenAIInvalidParameterError(
			"reasoning_effort",
			"kimi-k3 reasoning_effort must be one of: low, high, max",
		)
	}
	shaped := body
	var err error
	if mapped != effort {
		shaped, err = sjson.SetBytes(shaped, "reasoning_effort", mapped)
		if err != nil {
			return body, err
		}
	}
	mode := strings.ToLower(strings.TrimSpace(thinkingMode))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(gjson.GetBytes(shaped, "thinking.type").String()))
	}
	if mode == "disabled" {
		return shaped, nil
	}
	if mode == "enabled" || mode == "auto" {
		return shaped, nil
	}
	if !gjson.GetBytes(shaped, "thinking").IsObject() {
		shaped, err = sjson.DeleteBytes(shaped, "thinking")
		if err != nil {
			return body, err
		}
	}
	return sjson.SetBytes(shaped, "thinking.type", "enabled")
}

func normalizeVolcKimiAllowedTools(body []byte) ([]byte, error) {
	if gjson.GetBytes(body, "tool_choice.type").String() != "allowed_tools" {
		return body, nil
	}
	invalid := func(message string) ([]byte, error) {
		return nil, newOpenAIInvalidParameterError("tool_choice", message)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return invalid("invalid tool selection request")
	}
	if _, ok := root["functions"]; ok {
		return invalid("allowed_tools cannot be combined with legacy functions")
	}
	if _, ok := root["function_call"]; ok {
		return invalid("allowed_tools cannot be combined with legacy function_call")
	}
	var choice struct {
		Mode  string            `json:"mode"`
		Tools []json.RawMessage `json:"tools"`
	}
	selection := root["tool_choice"]
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(selection, &envelope); err != nil {
		return invalid("invalid allowed_tools selection")
	}
	if nested, ok := envelope["allowed_tools"]; ok {
		// Chat Completions nests this policy; accept the existing flattened
		// compatibility form only when the nested policy is absent.
		_, hasMode := envelope["mode"]
		_, hasTools := envelope["tools"]
		if hasMode || hasTools {
			return invalid("cannot combine nested and flattened allowed_tools fields")
		}
		selection = nested
	}
	if err := json.Unmarshal(selection, &choice); err != nil || (choice.Mode != "auto" && choice.Mode != "required") || len(choice.Tools) == 0 {
		return invalid("allowed_tools requires mode auto or required and a non-empty function list")
	}
	allowed := make(map[string]bool, len(choice.Tools))
	for _, raw := range choice.Tools {
		ref := gjson.ParseBytes(raw)
		name := ref.Get("function.name").String()
		flat := ref.Get("name").String()
		if name == "" {
			name = flat
		} else if flat != "" && flat != name {
			return invalid("conflicting allowed function names")
		}
		if ref.Get("type").String() != "function" || name == "" || allowed[name] {
			return invalid("allowed_tools requires unique named functions")
		}
		allowed[name] = true
	}
	var definitions []json.RawMessage
	if err := json.Unmarshal(root["tools"], &definitions); err != nil {
		return invalid("tools must be an array")
	}
	selected := make([]json.RawMessage, 0, len(allowed))
	seen := make(map[string]bool, len(allowed))
	for _, raw := range definitions {
		def := gjson.ParseBytes(raw)
		name := def.Get("function.name").String()
		if def.Get("type").String() != "function" || !allowed[name] {
			continue
		}
		if seen[name] {
			return invalid("allowed function must have exactly one definition")
		}
		seen[name] = true
		selected = append(selected, raw)
	}
	if len(seen) != len(allowed) {
		return invalid("allowed function must have exactly one definition")
	}
	var err error
	root["tools"], err = json.Marshal(selected)
	if err != nil {
		return nil, fmt.Errorf("encode allowed tools: %w", err)
	}
	root["tool_choice"], err = json.Marshal(choice.Mode)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}
