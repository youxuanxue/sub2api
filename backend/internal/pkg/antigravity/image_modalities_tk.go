package antigravity

// EnsureImageResponseModalities sets generationConfig.responseModalities to
// TEXT+IMAGE for image models when IMAGE is absent.
//
// Chat Completions / Messages → Gemini conversion never carries modalities.
// Without them cloudcode-pa returns empty content (token-only billing ~$0.0002)
// while the same account with modalities bills ~$0.10 and returns inlineData.
// Returns true when the request map was mutated.
func EnsureImageResponseModalities(request map[string]any, model string) bool {
	if request == nil || !IsImageModel(model) {
		return false
	}

	gen, _ := request["generationConfig"].(map[string]any)
	if gen == nil {
		if snake, ok := request["generation_config"].(map[string]any); ok {
			gen = snake
		}
	}
	if gen == nil {
		gen = make(map[string]any)
		request["generationConfig"] = gen
	}

	if modalitiesContainImage(gen["responseModalities"]) || modalitiesContainImage(gen["response_modalities"]) {
		return false
	}
	gen["responseModalities"] = []string{"TEXT", "IMAGE"}
	delete(gen, "response_modalities")
	return true
}

func modalitiesContainImage(raw any) bool {
	switch v := raw.(type) {
	case []string:
		for _, m := range v {
			if m == "IMAGE" {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == "IMAGE" {
				return true
			}
		}
	}
	return false
}
