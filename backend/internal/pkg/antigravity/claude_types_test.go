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

	if len(byID) == 0 {
		t.Fatal("DefaultModels must not be empty")
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
