package antigravity

// NormalizeGeminiResponseSchema translates SDK schema aliases to the v1internal
// responseSchema field. Response schemas must not gain tool-argument placeholders.
func NormalizeGeminiResponseSchema(payload map[string]any) (bool, error) {
	modified := false
	for _, key := range []string{"generationConfig", "generation_config"} {
		gen, ok := payload[key].(map[string]any)
		if !ok {
			continue
		}
		schema, exists := gen["responseSchema"]
		for _, alias := range []string{"response_schema", "responseJsonSchema", "response_json_schema"} {
			if value, found := gen[alias]; found {
				if !exists {
					schema, exists = value, true
				}
				delete(gen, alias)
				modified = true
			}
		}
		if !exists {
			continue
		}
		if object, ok := schema.(map[string]any); ok {
			cleaned, err := cleanJSONSchema(object, false)
			if err != nil {
				return false, err
			}
			schema = cleaned
			modified = true
		}
		gen["responseSchema"] = schema
	}
	return modified, nil
}

// NormalizeGeminiSystemIdentity uses the same narrowly anchored SDK metadata
// cleanup as the Claude transformer. User messages and later mentions are intact.
func NormalizeGeminiSystemIdentity(payload map[string]any) bool {
	modified := false
	for _, key := range []string{"systemInstruction", "system_instruction"} {
		instruction, ok := payload[key].(map[string]any)
		if !ok {
			continue
		}
		parts, _ := instruction["parts"].([]any)
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			text, ok := part["text"].(string)
			if !ok {
				continue
			}
			normalized := neutralizeClaudeIdentity(stripClaudeAttribution(text))
			if normalized != text {
				part["text"] = normalized
				modified = true
			}
		}
	}
	return modified
}
