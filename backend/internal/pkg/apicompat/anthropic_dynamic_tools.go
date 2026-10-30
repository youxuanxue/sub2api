package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Dynamic tool blocks are protocol nodes, never prose. Keep additions at their
// history position so loading a tool does not invalidate the prefix tools list.
// Responses has no evidenced removal carrier: reject rather than leave a tool
// callable while telling the client that its removal was honored.
// HasAnthropicDynamicToolChanges detects protocol events without decoding their
// payload, so malformed events are still validated instead of silently dropped.
func HasAnthropicDynamicToolChanges(req *AnthropicRequest) bool {
	if req == nil {
		return false
	}
	for _, raw := range append([]json.RawMessage{req.System}, anthropicMessageContents(req.Messages)...) {
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_addition" || b.Type == "tool_removal" {
				return true
			}
		}
	}
	return false
}

func anthropicMessageContents(msgs []AnthropicMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func convertAnthropicDynamicTools(req *AnthropicRequest) ([]ResponsesInputItem, []AnthropicTool, error) {
	if !HasAnthropicDynamicToolChanges(req) {
		input, err := convertAnthropicToResponsesInput(req.System, req.Messages)
		return input, req.Tools, err
	}
	var systemBlocks []struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(req.System, &systemBlocks)
	for _, b := range systemBlocks {
		if b.Type == "tool_addition" || b.Type == "tool_removal" {
			return nil, nil, fmt.Errorf("%s requires a system-role message, not the system prompt", b.Type)
		}
	}
	out, err := convertAnthropicToResponsesInput(req.System, nil)
	if err != nil {
		return nil, nil, err
	}
	initial := make([]AnthropicTool, 0, len(req.Tools))
	definitions := make(map[string][]AnthropicTool)
	known := make(map[string]ResponsesTool)
	for _, t := range req.Tools {
		definitions[t.Name] = append(definitions[t.Name], t)
		if !t.DeferLoading {
			initial = append(initial, t)
		}
		converted := convertAnthropicToolsToResponses([]AnthropicTool{t})
		known[t.Name] = converted[0]
	}
	pending := make(map[string]bool)
	var deferred []ResponsesInputItem
	for i, m := range req.Messages {
		var blocks []AnthropicContentBlock
		content := bytes.TrimSpace(m.Content)
		if len(content) > 0 && content[0] == '[' {
			if err := json.Unmarshal(m.Content, &blocks); err != nil {
				return nil, nil, fmt.Errorf("messages[%d]: invalid dynamic tool history: %w", i, err)
			}
		}
		for _, b := range blocks {
			if b.Type == "tool_removal" {
				return nil, nil, fmt.Errorf("messages[%d]: tool_removal cannot be represented by the Responses bridge", i)
			}
			if b.Type == "tool_addition" && m.Role != "system" {
				return nil, nil, fmt.Errorf("messages[%d]: tool_addition requires a system-role message", i)
			}
		}
		if m.Role == "system" {
			items, err := anthropicDynamicSystemItems(m.Content, definitions, known)
			if err != nil {
				return nil, nil, fmt.Errorf("messages[%d]: %w", i, err)
			}
			if len(pending) > 0 {
				deferred = append(deferred, items...)
			} else {
				out = append(out, items...)
			}
			continue
		}
		items, err := anthropicMsgToResponsesItems(m)
		if err != nil {
			return nil, nil, err
		}
		for _, item := range items {
			out = append(out, item)
			switch item.Type {
			case "function_call":
				pending[item.CallID] = true
			case "function_call_output":
				delete(pending, item.CallID)
				if len(pending) == 0 && len(deferred) > 0 {
					out = append(out, deferred...)
					deferred = nil
				}
			}
		}
	}
	if len(deferred) > 0 {
		return nil, nil, fmt.Errorf("dynamic tool changes require results for pending tool calls")
	}
	return out, initial, nil
}

func anthropicDynamicSystemItems(raw json.RawMessage, definitions map[string][]AnthropicTool, known map[string]ResponsesTool) ([]ResponsesInputItem, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw, _ = json.Marshal([]AnthropicContentBlock{{Type: "text", Text: text}})
	}
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	var out []ResponsesInputItem
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text == "" || isAnthropicBillingHeaderText(b.Text) {
				continue
			}
			content, _ := json.Marshal([]ResponsesContentPart{{Type: "input_text", Text: b.Text}})
			out = append(out, ResponsesInputItem{Type: "message", Role: "developer", Content: content})
		case "tool_addition":
			t, err := resolveAnthropicToolAddition(b.Tool, definitions)
			if err != nil {
				return nil, err
			}
			converted := convertAnthropicToolsToResponses([]AnthropicTool{t})[0]
			if old, ok := known[t.Name]; ok {
				a, _ := json.Marshal(old)
				z, _ := json.Marshal(converted)
				if !bytes.Equal(a, z) {
					return nil, fmt.Errorf("conflicting tool definition for %q", t.Name)
				}
			}
			known[t.Name] = converted
			out = append(out, ResponsesInputItem{Type: "additional_tools", Role: "developer", Tools: []ResponsesTool{converted}})
		default:
			return nil, fmt.Errorf("unsupported system content block %q in dynamic tool history", b.Type)
		}
	}
	return out, nil
}

func resolveAnthropicToolAddition(change *AnthropicToolChange, definitions map[string][]AnthropicTool) (AnthropicTool, error) {
	var t AnthropicTool
	if change == nil {
		return t, fmt.Errorf("tool_addition requires a tool payload")
	}
	switch change.Type {
	case "tool_definition":
		if change.Definition == nil || change.Name != "" {
			return t, fmt.Errorf("tool_definition requires a definition and no reference name")
		}
		t = *change.Definition
	case "tool_reference":
		if change.Definition != nil {
			return t, fmt.Errorf("tool_reference cannot include a definition")
		}
		candidates := definitions[change.Name]
		if len(candidates) == 0 {
			return t, fmt.Errorf("unknown tool_reference %q", change.Name)
		}
		if len(candidates) != 1 {
			return t, fmt.Errorf("ambiguous tool_reference %q", change.Name)
		}
		t = candidates[0]
	default:
		return t, fmt.Errorf("unsupported tool_addition payload %q", change.Type)
	}
	if strings.TrimSpace(t.Name) == "" {
		return t, fmt.Errorf("tool_addition requires a nonempty tool name")
	}
	if t.Type != "" && t.Type != "custom" {
		return t, fmt.Errorf("unsupported dynamic tool type %q", t.Type)
	}
	if len(t.InputSchema) > 0 {
		var schema map[string]any
		if json.Unmarshal(t.InputSchema, &schema) != nil || schema == nil {
			return t, fmt.Errorf("tool_addition input_schema must be an object")
		}
	}
	return t, nil
}
