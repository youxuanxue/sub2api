package antigravity

import "testing"

func TestDefaultModels_ContainsNewAndLegacyImageModels(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	byID := make(map[string]ClaudeModel, len(models))
	for _, m := range models {
		byID[m.ID] = m
	}

	requiredIDs := []string{
		"claude-opus-4-6-thinking",
		"claude-sonnet-4-6",
		"gemini-2.5-flash-image",
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview",
		"gemini-3-pro-image", // legacy compatibility
		"gemini-3.5-flash-low",
		"gemini-3.5-flash-extra-low",
		"gemini-3-flash-agent",
		"gemini-pro-agent",
	}

	for _, id := range requiredIDs {
		if _, ok := byID[id]; !ok {
			t.Fatalf("expected model %q to be exposed in DefaultModels", id)
		}
	}

	unavailableIDs := []string{
		"claude-fable-5",
		"claude-opus-4-8",
		"claude-sonnet-5",
		"gpt-oss-120b-medium",
		"gemini-2.5-flash-image-preview",
		"gemini-3-pro-preview",
		"gemini-3.1-pro-high",
	}
	for _, id := range unavailableIDs {
		if _, ok := byID[id]; ok {
			t.Fatalf("live-unavailable Antigravity model %q must not be exposed in DefaultModels", id)
		}
	}
}
