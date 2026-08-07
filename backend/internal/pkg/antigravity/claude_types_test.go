package antigravity

import "testing"

func TestDefaultModels_StructuralMetadata(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	byID := make(map[string]ClaudeModel, len(models))
	for _, m := range models {
		if m.ID == "" {
			t.Fatal("default model IDs must not be empty")
		}
		if m.Type != "model" {
			t.Fatalf("default model %q must use type=model", m.ID)
		}
		if m.DisplayName == "" {
			t.Fatalf("default model %q must have display metadata", m.ID)
		}
		if _, exists := byID[m.ID]; exists {
			t.Fatalf("duplicate default model %q", m.ID)
		}
		byID[m.ID] = m
	}

	requiredIDs := []string{
		"claude-fable-5",
		"claude-opus-4-8",
		"claude-opus-4-6-thinking",
		"gemini-2.5-flash-image",
		"gemini-2.5-flash-image-preview",
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview",
		"gemini-3-pro-image", // legacy compatibility
		"gemini-3.6-flash",
		"gemini-3.6-flash-high",
		"gemini-3.6-flash-low",
		"gemini-3.6-flash-medium",
		"gemini-3.6-flash-tiered",
	}
	for _, id := range requiredIDs {
		if _, ok := byID[id]; !ok {
			t.Fatalf("DefaultModels must include %q", id)
		}
	}
}

func TestDefaultGeminiModels_UsesGeminiShape(t *testing.T) {
	t.Parallel()

	models := DefaultGeminiModels()
	if len(models) == 0 {
		t.Fatal("DefaultGeminiModels must not be empty")
	}
	for _, m := range models {
		if m.Name == "" || len(m.Name) < len("models/") || m.Name[:len("models/")] != "models/" {
			t.Fatalf("Gemini model must use models/ name shape, got %+v", m)
		}
		if len(m.SupportedGenerationMethods) == 0 {
			t.Fatalf("Gemini model %q must advertise generation methods", m.Name)
		}
	}
}
