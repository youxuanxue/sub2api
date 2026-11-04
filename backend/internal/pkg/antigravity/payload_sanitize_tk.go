package antigravity

import "strings"

// SanitizeGeminiNativePayload removes dirty client fields, broken inlineData,
// and truly empty part objects ({}). An instruction left with no parts is
// omitted. It never strips generationConfig.responseModalities (required for
// image generation). Returns true if the payload was modified.
func SanitizeGeminiNativePayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	modified := DeepCleanUndefined(payload) > 0

	for _, key := range []string{"session_id", "requestType", "request_type", "project", "userAgent", "user_agent"} {
		if _, ok := payload[key]; ok {
			delete(payload, key)
			modified = true
		}
	}

	if contents, ok := payload["contents"].([]any); ok {
		for _, c := range contents {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			parts, ok := cm["parts"].([]any)
			if !ok {
				continue
			}
			cleaned := make([]any, 0, len(parts))
			for _, p := range parts {
				pm, ok := p.(map[string]any)
				if !ok {
					cleaned = append(cleaned, p)
					continue
				}
				if dropBrokenInlineData(pm) {
					modified = true
					continue
				}
				cleaned = append(cleaned, pm)
			}
			if len(cleaned) != len(parts) {
				cm["parts"] = cleaned
				modified = true
			}
		}
	}

	if DropEmptyGeminiParts(payload) {
		modified = true
	}

	return modified
}

// DropEmptyGeminiParts removes part objects that have no fields at all ({}).
// text, inlineData, fileData, functionCall, functionResponse, thoughtSignature,
// and any other real field keep the part, even when the value looks blank.
// systemInstruction / system_instruction is omitted only when no parts remain.
// A contents entry is dropped only when no parts remain. CLIProxyAPI#7136.
func DropEmptyGeminiParts(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	modified := false
	for _, key := range []string{"systemInstruction", "system_instruction"} {
		if omitEmptyGeminiInstruction(payload, key) {
			modified = true
		}
	}
	if omitEmptyGeminiContentMessages(payload) {
		modified = true
	}
	return modified
}

func omitEmptyGeminiInstruction(payload map[string]any, key string) bool {
	inst, ok := payload[key].(map[string]any)
	if !ok {
		return false
	}
	parts, ok := inst["parts"].([]any)
	if !ok {
		return false
	}
	cleaned := contentfulGeminiParts(parts)
	if len(cleaned) == 0 {
		delete(payload, key)
		return true
	}
	if len(cleaned) != len(parts) {
		inst["parts"] = cleaned
		return true
	}
	return false
}

func omitEmptyGeminiContentMessages(payload map[string]any) bool {
	contents, ok := payload["contents"].([]any)
	if !ok || len(contents) == 0 {
		return false
	}
	filtered := make([]any, 0, len(contents))
	modified := false
	for _, c := range contents {
		contentMap, ok := c.(map[string]any)
		if !ok {
			filtered = append(filtered, c)
			continue
		}
		parts, hasParts := contentMap["parts"]
		if !hasParts {
			filtered = append(filtered, contentMap)
			continue
		}
		partsSlice, ok := parts.([]any)
		if !ok {
			filtered = append(filtered, contentMap)
			continue
		}
		cleaned := contentfulGeminiParts(partsSlice)
		if len(cleaned) == 0 {
			modified = true
			continue
		}
		if len(cleaned) != len(partsSlice) {
			contentMap["parts"] = cleaned
			modified = true
		}
		filtered = append(filtered, contentMap)
	}
	if modified {
		payload["contents"] = filtered
	}
	return modified
}

func contentfulGeminiParts(parts []any) []any {
	cleaned := make([]any, 0, len(parts))
	for _, p := range parts {
		if pm, ok := p.(map[string]any); ok && !geminiPartHasContent(pm) {
			continue
		}
		cleaned = append(cleaned, p)
	}
	return cleaned
}

func geminiPartHasContent(part map[string]any) bool {
	return len(part) > 0
}

func dropBrokenInlineData(part map[string]any) bool {
	var inline map[string]any
	if v, ok := part["inlineData"].(map[string]any); ok {
		inline = v
	} else if v, ok := part["inline_data"].(map[string]any); ok {
		inline = v
	} else {
		return false
	}
	mime, _ := firstString(inline, "mimeType", "mime_type")
	data, _ := firstString(inline, "data")
	mime = strings.TrimSpace(mime)
	data = strings.TrimSpace(data)
	if mime == "" || data == "" || data == "[undefined]" {
		return true
	}
	return false
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			return s, true
		}
	}
	return "", false
}
