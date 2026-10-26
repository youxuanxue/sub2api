package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyMoonshotK3DynamicToolsShape promotes Moonshot-style dynamic tool injection
// (system message carrying tools without content) into top-level tools and drops
// those messages.
//
// Official Moonshot accepts that message shape. Volc Agent Plan (china kimi-k3
// supply) rejects it with missing `*.content`. Promoting keeps Calculator-style
// admission probes working across K3 supplies without requiring clients to
// change payload shape. When the client omitted tool_choice, set "required" so
// the promoted tools are actually invoked instead of the model answering inline.
func applyMoonshotK3DynamicToolsShape(model string, body []byte) []byte {
	if len(body) == 0 || !isNewAPIAliFixedSamplingModel(model) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() || len(messages.Array()) == 0 {
		return body
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
		return body
	}

	allTools := make([]any, 0)
	if existing := gjson.GetBytes(body, "tools"); existing.IsArray() {
		for _, tool := range existing.Array() {
			allTools = append(allTools, tool.Value())
		}
	}
	// A client may declare the same tool both at top level and in the dynamic
	// system message; promoting blindly sent two identical declarations
	// upstream. First writer wins. Owner: apicompat.AppendRawResponsesToolsDeduped
	// (handles the Chat Completions {"function":{"name":...}} nesting used here).
	allTools, _ = apicompat.AppendRawResponsesToolsDeduped(allTools, promoted...)

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
	return shaped
}

func isMoonshotDynamicToolSystemMessage(msg gjson.Result) bool {
	if !strings.EqualFold(msg.Get("role").String(), "system") {
		return false
	}
	tools := msg.Get("tools")
	if !tools.Exists() || !tools.IsArray() || len(tools.Array()) == 0 {
		return false
	}
	content := msg.Get("content")
	// Official Moonshot: system+tools must not carry a content field.
	// Absent, JSON null, or empty/whitespace string → promote. Non-empty
	// content stays as client-authored (admission probes and mixed system turns).
	if !content.Exists() || content.Type == gjson.Null {
		return true
	}
	return content.Type == gjson.String && strings.TrimSpace(content.String()) == ""
}
