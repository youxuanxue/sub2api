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

	// Converged Google public request surface advertised by /antigravity models.
	for _, id := range []string{
		"gemini-3.6-flash",
		"gemini-3.7-flash",
		"gemini-3.8-flash",
		"gemini-3-flash-preview",
		"gemini-3.5-flash-lite",
		"gemini-3.1-flash-image",
		"gemini-3-pro-image",
	} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("DefaultModels missing converged id %q", id)
		}
	}
	for _, retired := range []string{"gemini-2.5-flash", "gemini-2.5-pro", "gemini-pro-agent", "gemini-3-flash"} {
		if _, ok := byID[retired]; ok {
			t.Fatalf("DefaultModels must not advertise retired id %q", retired)
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
