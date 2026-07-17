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
		if len(model.SupportedGenerationMethods) == 0 {
			t.Fatalf("fallback model %q must advertise generation methods", model.Name)
		}
		if _, exists := byName[model.Name]; exists {
			t.Fatalf("duplicate fallback model %q", model.Name)
		}
		byName[model.Name] = model
	}

	if len(byName) == 0 {
		t.Fatal("DefaultModels must not be empty")
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
