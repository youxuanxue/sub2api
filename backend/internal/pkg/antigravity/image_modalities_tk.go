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

	gen := ensureGeminiGenerationConfigMap(request)
	if modalitiesContainImage(gen["responseModalities"]) || modalitiesContainImage(gen["response_modalities"]) {
		return false
	}
	gen["responseModalities"] = []string{"TEXT", "IMAGE"}
	delete(gen, "response_modalities")
	return true
}

// OmitImageModelMaxOutputTokens drops generationConfig.maxOutputTokens for
// native image models. Chat/Messages clients often send a small max_tokens
// (probe default 32); when forwarded as maxOutputTokens, cloudcode-pa starves
// image_gen — empty assistant content while the gateway still billed ImageCount=1.
// Admin image tests already omit this field; Images API has no max_tokens.
// Returns true when a limit field was removed.
func OmitImageModelMaxOutputTokens(request map[string]any, model string) bool {
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
		return false
	}
	removed := false
	for _, key := range []string{"maxOutputTokens", "max_output_tokens"} {
		if _, ok := gen[key]; ok {
			delete(gen, key)
			removed = true
		}
	}
	return removed
}

func ensureGeminiGenerationConfigMap(request map[string]any) map[string]any {
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
	return gen
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
