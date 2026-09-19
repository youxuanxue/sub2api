package antigravity

import "testing"

func TestSanitizeGeminiNativePayload_DropsBrokenInlineDataKeepsModalities(t *testing.T) {
	t.Parallel()
	payload := map[string]any{
		"session_id": "should-drop",
		"contents": []any{
			map[string]any{
				"role": "user",
				"parts": []any{
					map[string]any{"text": "hi"},
					map[string]any{
						"inlineData": map[string]any{
							"mimeType": "image/png",
							"data":     "[undefined]",
						},
					},
				},
			},
		},
		"generationConfig": map[string]any{
			"responseModalities": []any{"TEXT", "IMAGE"},
		},
	}
	if !SanitizeGeminiNativePayload(payload) {
		t.Fatal("expected modification")
	}
	if _, ok := payload["session_id"]; ok {
		t.Fatal("session_id should be removed")
	}
	contents, ok := payload["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents: %#v", payload["contents"])
	}
	cm, ok := contents[0].(map[string]any)
	if !ok {
		t.Fatal("content not map")
	}
	parts, ok := cm["parts"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("want 1 part after dropping broken inlineData, got %#v", cm["parts"])
	}
	gen, ok := payload["generationConfig"].(map[string]any)
	if !ok {
		t.Fatal("generationConfig missing")
	}
	mods, ok := gen["responseModalities"].([]any)
	if !ok || len(mods) != 2 {
		t.Fatalf("responseModalities must be preserved, got %#v", gen["responseModalities"])
	}
}

func TestSanitizeGeminiNativePayload_NoOpClean(t *testing.T) {
	t.Parallel()
	payload := map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": "ok"}},
			},
		},
	}
	if SanitizeGeminiNativePayload(payload) {
		t.Fatal("clean payload should not report modification")
	}
}
