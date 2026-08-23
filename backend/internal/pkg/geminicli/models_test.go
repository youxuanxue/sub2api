package geminicli

import "testing"

func TestDefaultModels_StructuralMetadata(t *testing.T) {
	t.Parallel()

	byID := make(map[string]Model, len(DefaultModels))
	for _, model := range DefaultModels {
		if model.ID == "" {
			t.Fatal("default model IDs must not be empty")
		}
		if model.Type != "model" {
			t.Fatalf("default model %q must use type=model", model.ID)
		}
		if model.DisplayName == "" {
			t.Fatalf("default model %q must have display metadata", model.ID)
		}
		if _, exists := byID[model.ID]; exists {
			t.Fatalf("duplicate default model %q", model.ID)
		}
		byID[model.ID] = model
	}

	if _, ok := byID[DefaultTestModel]; !ok {
		t.Fatalf("DefaultTestModel %q must be present in DefaultModels", DefaultTestModel)
	}
}

func TestModelsForIDs_PrefersCanonicalAndSynthesizesMissingIDs(t *testing.T) {
	t.Parallel()

	if len(DefaultModels) == 0 {
		t.Fatal("DefaultModels must not be empty")
	}
	canonical := DefaultModels[0]
	const synthesizedID = "gemini-test-owner-boundary-zzz"

	got := ModelsForIDs([]string{canonical.ID, synthesizedID})
	if len(got) != 2 {
		t.Fatalf("ModelsForIDs returned %d models, want 2", len(got))
	}
	if got[0] != canonical {
		t.Fatalf("ModelsForIDs(%q) = %+v, want canonical %+v", canonical.ID, got[0], canonical)
	}
	if got[1].ID != synthesizedID || got[1].Type != "model" || got[1].DisplayName != synthesizedID {
		t.Fatalf("ModelsForIDs must synthesize metadata for unknown ids, got %+v", got[1])
	}
}

func TestGoogleOneModels_ExcludeUnsupportedNewAndImageModels(t *testing.T) {
	t.Parallel()

	mapping := GoogleOneModelMapping()
	for _, id := range []string{"gemini-2.0-flash", "gemini-2.5-flash", "gemini-2.5-pro"} {
		if mapping[id] != id {
			t.Fatalf("expected Google One model %q to map to itself", id)
		}
	}
	for _, id := range []string{"gemini-2.5-flash-image", "gemini-3.1-flash-image", "gemini-3.5-flash"} {
		if _, ok := mapping[id]; ok {
			t.Fatalf("did not expect unsupported Google One model %q", id)
		}
	}

	mapping["gemini-2.5-flash"] = "mutated"
	if GoogleOneModelMapping()["gemini-2.5-flash"] != "gemini-2.5-flash" {
		t.Fatal("GoogleOneModelMapping must return a defensive copy")
	}
}
