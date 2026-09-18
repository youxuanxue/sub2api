package antigravity

import "encoding/json"

// ResolveV1InternalRequestType decides the cloudcode-pa requestType for
// v1internal generateContent / streamGenerateContent envelopes.
//
// Plain text omits requestType so Google does not force the agent pool
// (Manager Sandbox→Daily→Prod behavior). Image models keep image_gen;
// web_search and tool/agent turns keep their explicit types.
func ResolveV1InternalRequestType(model string, hasWebSearch, hasTools, hasToolInteractions bool) string {
	if IsImageModel(model) {
		return "image_gen"
	}
	if hasWebSearch {
		return "web_search"
	}
	if hasTools || hasToolInteractions {
		return "agent"
	}
	return ""
}

// GeminiRequestHasWebSearch reports googleSearch / google_search tool entries
// on a Gemini-native generateContent body.
func GeminiRequestHasWebSearch(request map[string]any) bool {
	if request == nil {
		return false
	}
	tools, ok := request["tools"].([]any)
	if !ok || len(tools) == 0 {
		return false
	}
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if _, ok := tm["googleSearch"]; ok {
			return true
		}
		if _, ok := tm["google_search"]; ok {
			return true
		}
	}
	return false
}

// GeminiRequestHasTools reports whether a Gemini-native generateContent body
// declares any functionDeclarations / function_declarations.
func GeminiRequestHasTools(request map[string]any) bool {
	if request == nil {
		return false
	}
	tools, ok := request["tools"].([]any)
	if !ok || len(tools) == 0 {
		return false
	}
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if decls, ok := tm["functionDeclarations"].([]any); ok && len(decls) > 0 {
			return true
		}
		if decls, ok := tm["function_declarations"].([]any); ok && len(decls) > 0 {
			return true
		}
	}
	return false
}

// GeminiRequestHasToolInteractions reports prior functionCall / functionResponse
// turns in contents (so follow-up agent turns keep requestType=agent).
func GeminiRequestHasToolInteractions(request map[string]any) bool {
	if request == nil {
		return false
	}
	contents, ok := request["contents"].([]any)
	if !ok {
		return false
	}
	for _, c := range contents {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := cm["parts"].([]any)
		if !ok {
			continue
		}
		for _, p := range parts {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if _, ok := pm["functionCall"]; ok {
				return true
			}
			if _, ok := pm["function_call"]; ok {
				return true
			}
			if _, ok := pm["functionResponse"]; ok {
				return true
			}
			if _, ok := pm["function_response"]; ok {
				return true
			}
		}
	}
	return false
}

// ClaudeRequestHasToolInteractions reports tool_use / tool_result blocks in
// Anthropic Messages so Claude→Gemini transform keeps requestType=agent.
func ClaudeRequestHasToolInteractions(messages []ClaudeMessage) bool {
	for _, msg := range messages {
		if len(msg.Content) == 0 {
			continue
		}
		// Content may be a plain string or an array of blocks.
		var blocks []map[string]any
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}
		for _, block := range blocks {
			t, _ := block["type"].(string)
			if t == "tool_use" || t == "tool_result" {
				return true
			}
		}
	}
	return false
}
