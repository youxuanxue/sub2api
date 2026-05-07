package handler

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// SetModelListFilter wires the optional client model-list filter into
// GatewayHandler without changing the upstream constructor signature. Called
// during Wire DI setup; absent call = filter disabled (FilterClientFacing is
// nil-safe → fail-open, returns candidates unchanged).
//
// Per docs/approved/pricing-availability-source-of-truth.md §2.5 (Goal 2,
// R-003). Mirrors GatewayService.SetPricingAvailabilityService in shape.
func (h *GatewayHandler) SetModelListFilter(f *service.ModelListFilter) {
	if h != nil {
		h.tkModelListFilter = f
	}
}

// HasModelListFilter returns true once the model-list filter is wired. Used
// by wire_assertion_tk_test.go to prove production DI ran the setter.
func (h *GatewayHandler) HasModelListFilter() bool {
	return h != nil && h.tkModelListFilter != nil
}

// tkFilterModelIDs applies the model-list filter to a slice of model IDs and
// returns the filtered set. Safe to call when h.tkModelListFilter is nil
// (fail-open → returns ids unchanged).
func (h *GatewayHandler) tkFilterModelIDs(ctx context.Context, platform string, ids []string) []string {
	if h == nil || h.tkModelListFilter == nil {
		return ids
	}
	return h.tkModelListFilter.FilterClientFacing(ctx, platform, ids)
}

// tkOpenAIDefaultModelIDs returns the default OpenAI model IDs as a string slice,
// optionally filtered by the model-list filter.
func (h *GatewayHandler) tkOpenAIDefaultModelIDs(ctx context.Context, platform string) []openai.Model {
	ids := make([]string, len(openai.DefaultModels))
	for i, m := range openai.DefaultModels {
		ids[i] = m.ID
	}
	filtered := h.tkFilterModelIDs(ctx, platform, ids)
	out := make([]openai.Model, 0, len(filtered))
	filtSet := make(map[string]bool, len(filtered))
	for _, id := range filtered {
		filtSet[id] = true
	}
	for _, m := range openai.DefaultModels {
		if filtSet[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

// tkClaudeDefaultModelIDs returns the default Claude model IDs as a string slice,
// optionally filtered by the model-list filter.
func (h *GatewayHandler) tkClaudeDefaultModelIDs(ctx context.Context, platform string) []claude.Model {
	ids := make([]string, len(claude.DefaultModels))
	for i, m := range claude.DefaultModels {
		ids[i] = m.ID
	}
	filtered := h.tkFilterModelIDs(ctx, platform, ids)
	out := make([]claude.Model, 0, len(filtered))
	filtSet := make(map[string]bool, len(filtered))
	for _, id := range filtered {
		filtSet[id] = true
	}
	for _, m := range claude.DefaultModels {
		if filtSet[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

// tkAntigravityDefaultModels filters antigravity.DefaultModels() by pricing +
// availability and returns []antigravity.ClaudeModel preserving the original
// response shape. platform is always service.PlatformAntigravity.
//
// Fixes review finding R-001: AntigravityModels must keep []ClaudeModel shape
// and must not substitute the full cross-platform catalog for the antigravity-
// only candidate set.
func (h *GatewayHandler) tkAntigravityDefaultModels(ctx context.Context) []antigravity.ClaudeModel {
	defaults := antigravity.DefaultModels()
	ids := make([]string, len(defaults))
	for i, m := range defaults {
		ids[i] = m.ID
	}
	filtered := h.tkFilterModelIDs(ctx, service.PlatformAntigravity, ids)
	filtSet := make(map[string]bool, len(filtered))
	for _, id := range filtered {
		filtSet[id] = true
	}
	out := make([]antigravity.ClaudeModel, 0, len(filtered))
	for _, m := range defaults {
		if filtSet[m.ID] {
			out = append(out, m)
		}
	}
	return out
}

// tkGeminiFallbackModelsList filters gemini.DefaultModels() by pricing +
// availability and returns a gemini.ModelsListResponse preserving the original
// response shape. Used by GeminiV1BetaListModels fallback paths.
//
// Fixes review finding CF-001: GeminiV1BetaListModels fallback paths must have
// a thin filter injection per design doc §2.5.
//
// Note: gemini.Model.Name uses the "models/<id>" prefix; we strip the prefix
// for pricing/availability lookup and restore it in the output.
func (h *GatewayHandler) tkGeminiFallbackModelsList(ctx context.Context) gemini.ModelsListResponse {
	defaults := gemini.DefaultModels()
	ids := make([]string, len(defaults))
	for i, m := range defaults {
		ids[i] = strings.TrimPrefix(m.Name, "models/")
	}
	filtered := h.tkFilterModelIDs(ctx, service.PlatformGemini, ids)
	filtSet := make(map[string]bool, len(filtered))
	for _, id := range filtered {
		filtSet[id] = true
	}
	out := make([]gemini.Model, 0, len(filtered))
	for _, m := range defaults {
		if filtSet[strings.TrimPrefix(m.Name, "models/")] {
			out = append(out, m)
		}
	}
	return gemini.ModelsListResponse{Models: out}
}
