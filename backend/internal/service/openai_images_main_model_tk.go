package service

import (
	"context"
	"os"
	"strings"
)

// Default Responses driver fallbacks when ChatGPT Codex rejects the preferred
// main model. Preferred model comes from SUB2API_IMAGES_MAIN_MODEL or
// openAIImagesResponsesMainModel; this list is tried next on the same account
// without cooling image capacity.
// Prefer cooler Codex-supported drivers (prod 2026-10-20 upstream probe):
// ChatGPT OAuth rejects mini/nano/5.5; hot text slots (6.1-sol/astra) stay out of
// the default image-driver chain so image traffic does not amplify text overload.
var openAIImagesMainModelFallbacks = []string{
	"gpt-6-sol",
	"gpt-5.6-luna",
	"gpt-6-luna",
}

type openAIImagesMainModelOverrideKey struct{}
type openAIImagesTriedMainModelsKey struct{}

func withOpenAIImagesMainModelOverride(ctx context.Context, model string) context.Context {
	model = strings.TrimSpace(model)
	if ctx == nil {
		ctx = context.Background()
	}
	if model == "" {
		return ctx
	}
	return context.WithValue(ctx, openAIImagesMainModelOverrideKey{}, model)
}

func openAIImagesMainModelOverride(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	model, _ := ctx.Value(openAIImagesMainModelOverrideKey{}).(string)
	return strings.TrimSpace(model)
}

func openAIImagesEffectiveMainModel(ctx context.Context) string {
	if override := openAIImagesMainModelOverride(ctx); override != "" {
		return override
	}
	return openAIImagesResponsesMainModelValue()
}

func openAIImagesTriedMainModels(ctx context.Context) map[string]bool {
	if ctx == nil {
		return nil
	}
	tried, _ := ctx.Value(openAIImagesTriedMainModelsKey{}).(map[string]bool)
	return tried
}

func withOpenAIImagesTriedMainModel(ctx context.Context, model string) context.Context {
	model = strings.ToLower(strings.TrimSpace(model))
	if ctx == nil {
		ctx = context.Background()
	}
	if model == "" {
		return ctx
	}
	prev := openAIImagesTriedMainModels(ctx)
	next := make(map[string]bool, len(prev)+1)
	for k, v := range prev {
		if v {
			next[k] = true
		}
	}
	next[model] = true
	return context.WithValue(ctx, openAIImagesTriedMainModelsKey{}, next)
}

func openAIImagesMainModelCandidates() []string {
	seen := make(map[string]bool, len(openAIImagesMainModelFallbacks)+2)
	out := make([]string, 0, len(openAIImagesMainModelFallbacks)+2)
	appendCandidate := func(raw string) {
		model := strings.TrimSpace(raw)
		if model == "" {
			return
		}
		key := strings.ToLower(model)
		if seen[key] || strings.HasPrefix(key, "gpt-image-") {
			return
		}
		seen[key] = true
		out = append(out, model)
	}
	appendCandidate(openAIImagesResponsesMainModelValue())
	if raw := strings.TrimSpace(os.Getenv("SUB2API_IMAGES_MAIN_MODEL_FALLBACKS")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			appendCandidate(part)
		}
		return out
	}
	for _, candidate := range openAIImagesMainModelFallbacks {
		appendCandidate(candidate)
	}
	return out
}

// nextOpenAIImagesMainModelAfterUnsupported picks the next unused driver after
// ChatGPT rejected the current Responses main model.
func nextOpenAIImagesMainModelAfterUnsupported(current string, tried map[string]bool) (string, bool) {
	copied := make(map[string]bool, len(tried)+1)
	for key, value := range tried {
		if value {
			copied[key] = true
		}
	}
	currentKey := strings.ToLower(strings.TrimSpace(current))
	if currentKey != "" {
		copied[currentKey] = true
	}
	for _, candidate := range openAIImagesMainModelCandidates() {
		if !copied[strings.ToLower(candidate)] {
			return candidate, true
		}
	}
	return "", false
}
