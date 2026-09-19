package antigravity

import "strings"

// SanitizeGeminiNativePayload removes dirty client fields and broken inlineData
// parts that cloudcode-pa rejects. It never strips generationConfig.responseModalities
// (required for image generation). Returns true if the payload was modified.
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

	return modified
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
