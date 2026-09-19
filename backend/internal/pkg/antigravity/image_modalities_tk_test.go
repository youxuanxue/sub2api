package antigravity

import "testing"

func TestEnsureImageResponseModalities_InjectsForImageModel(t *testing.T) {
	req := map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "draw"}}}},
	}
	if !EnsureImageResponseModalities(req, "gemini-3.1-flash-image") {
		t.Fatal("expected mutation for image model without modalities")
	}
	gen, ok := req["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("generationConfig missing")
	}
	mods, ok := gen["responseModalities"].([]string)
	if !ok || len(mods) != 2 || mods[0] != "TEXT" || mods[1] != "IMAGE" {
		t.Fatalf("modalities=%v", gen["responseModalities"])
	}
}

func TestEnsureImageResponseModalities_NoopWhenImagePresent(t *testing.T) {
	req := map[string]any{
		"generationConfig": map[string]any{
			"responseModalities": []any{"TEXT", "IMAGE"},
		},
	}
	if EnsureImageResponseModalities(req, "gemini-3.1-flash-image") {
		t.Fatal("should not mutate when IMAGE already present")
	}
}

func TestEnsureImageResponseModalities_NoopForTextModel(t *testing.T) {
	req := map[string]any{}
	if EnsureImageResponseModalities(req, "gemini-3.8-flash") {
		t.Fatal("text model must not get image modalities")
	}
	if _, ok := req["generationConfig"]; ok {
		t.Fatal("text model must not create generationConfig")
	}
}

func TestEnsureImageResponseModalities_NanoAlias(t *testing.T) {
	req := map[string]any{}
	if !EnsureImageResponseModalities(req, "nano-2") {
		t.Fatal("nano-2 is an image model alias")
	}
}
