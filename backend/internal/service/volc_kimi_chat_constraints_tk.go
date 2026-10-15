package service

import (
	"encoding/json"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// normalizeVolcKimiChatRequest owns the evidenced Volc Agent Plan Kimi K3
// thinking/tool contract. Moonshot's always-thinking policy is not portable to
// this supplier. See docs/approved/volc-kimi-chat-compatibility.md.
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
	// and works with Volc Kimi's default thinking (named selection returns 400).
	if gjson.GetBytes(shaped, "tool_choice.type").String() != "function" {
		return shaped, nil
	}
	if gjson.GetBytes(shaped, "function_call").Exists() || gjson.GetBytes(shaped, "functions").Exists() {
		return nil, newOpenAIInvalidParameterError("tool_choice", "cannot combine named tool_choice with legacy function fields on Volc Kimi")
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
	if selected == "" {
		return body, nil
	}
	shaped := body
	var err error
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
	}
	return shaped, err
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
	if err := json.Unmarshal(root["tool_choice"], &choice); err != nil || (choice.Mode != "auto" && choice.Mode != "required") || len(choice.Tools) == 0 {
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
