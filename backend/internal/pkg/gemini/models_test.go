package gemini

import "testing"

func TestDefaultModels_StructuralMetadata(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	byName := make(map[string]Model, len(models))
	for _, model := range models {
		if model.Name == "" {
			t.Fatal("fallback model names must not be empty")
		}
		if len(model.Name) < len("models/") || model.Name[:len("models/")] != "models/" {
			t.Fatalf("fallback model %q must use models/ prefix", model.Name)
		}
		if _, exists := byName[model.Name]; exists {
			t.Fatalf("duplicate fallback model %q", model.Name)
		}
		byName[model.Name] = model
	}

	if len(byName) == 0 {
		t.Fatal("DefaultModels must not be empty")
	}
	for _, id := range []string{
		"models/gemini-3.8-flash",
		"models/gemini-3.6-flash",
		"models/gemini-embedding-001",
		"models/veo-3.1-generate-001",
	} {
		if _, ok := byName[id]; !ok {
			t.Fatalf("DefaultModels missing converged id %q", id)
		}
	}
	for _, retired := range []string{
		"models/gemini-2.5-flash",
		"models/gemini-2.5-pro",
		"models/gemini-2.0-flash",
		"models/gemini-3.1-flash-image",
	} {
		if _, ok := byName[retired]; ok {
			t.Fatalf("DefaultModels must not advertise retired id %q", retired)
		}
	}
	if methods := byName["models/gemini-3.8-flash"].SupportedGenerationMethods; len(methods) == 0 {
		t.Fatal("text chat fallback must advertise generateContent methods")
	}
	if methods := byName["models/veo-3.1-generate-001"].SupportedGenerationMethods; len(methods) != 0 {
		t.Fatalf("veo fallback must not claim generateContent, got %v", methods)
	}
}

func TestHasFallbackModel_RecognizesDefaultModel(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	if len(models) == 0 {
		t.Fatal("DefaultModels must not be empty")
	}
	name := models[0].Name
	if !HasFallbackModel(name) {
		t.Fatalf("expected prefixed fallback model %q to be recognized", name)
	}
	if !HasFallbackModel(name[len("models/"):]) {
		t.Fatalf("expected unprefixed fallback model %q to be recognized", name)
	}
	if HasFallbackModel("gemini-unknown") {
		t.Fatalf("did not expect unknown model to exist in fallback catalog")
	}
}
