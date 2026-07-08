package geminicli

import "testing"

func TestDefaultModels_ContainsServableModels(t *testing.T) {
	t.Parallel()

	byID := make(map[string]Model, len(DefaultModels))
	for _, model := range DefaultModels {
		byID[model.ID] = model
	}

	required := []string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-2.5-pro",
		"imagen-4.0-generate-001",
		"veo-3.1-generate-001",
	}

	for _, id := range required {
		if _, ok := byID[id]; !ok {
			t.Fatalf("expected curated Gemini model %q to exist", id)
		}
	}
	for _, dead := range []string{"gemini-2.0-flash", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-3.5-flash"} {
		if _, ok := byID[dead]; ok {
			t.Fatalf("unservable Gemini model %q must not exist in DefaultModels", dead)
		}
	}
}
