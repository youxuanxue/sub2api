package handler

import (
	"context"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
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

// servableIDs is the gateway-side bridge to the unified client-facing servable
// truth (service.ServableClientFacingIDs via the wired model-list filter): the
// per-platform empirical allowlist (or canonical when unprobed), pruned of
// structurally-gone ids and filtered to priced. Every /v1/models-family FALLBACK
// sources its ids here, so the gateway advertises exactly the set the public
// /pricing catalog and the Your-Menu fallback show — no advertised_dead (a priced
// id not in the allowlist, e.g. gpt-5-pro), no visible-but-unpriced. Nil-safe: no
// filter wired → service.ServableClientFacingIDs fail-opens to the candidate set.
func (h *GatewayHandler) servableIDs(ctx context.Context, platform string) []string {
	if h == nil || h.tkModelListFilter == nil {
		return service.ServableClientFacingIDs(ctx, platform, nil, nil)
	}
	return h.tkModelListFilter.ServableClientFacingIDs(ctx, platform)
}

// tkUniversalModelIDs returns the metadata model list for a universal API key.
// Universal request routing is model-driven, so GET /v1/models deliberately skips
// the resolver and has no single backing group. The list must therefore be the
// union of the key owner's entitled groups, not GatewayService.GetAvailableModels
// with groupID=nil (that is the global schedulable account pool).
func (h *GatewayHandler) tkUniversalModelIDs(ctx context.Context, apiKey *service.APIKey, forcedPlatform string) ([]string, bool) {
	if h == nil || h.apiKeyService == nil || h.gatewayService == nil || apiKey == nil || !apiKey.IsUniversal() {
		return nil, false
	}
	groups, err := h.apiKeyService.GetAvailableGroups(ctx, apiKey.UserID)
	if err != nil {
		return nil, true
	}
	modelSet := make(map[string]struct{})
	for _, group := range groups {
		if strings.HasPrefix(group.Name, "__tk_probe_") {
			continue
		}
		if forcedPlatform != "" && group.Platform != forcedPlatform {
			continue
		}
		groupID := group.ID
		ids := h.gatewayService.GetAvailableModels(ctx, &groupID, group.Platform)
		ids = h.tkFilterModelIDs(ctx, group.Platform, ids)
		if len(ids) == 0 {
			ids = h.servableIDs(ctx, group.Platform)
		}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" {
				modelSet[id] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(modelSet))
	for id := range modelSet {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, true
}

// tkOpenAIDefaultModelIDs returns the /v1/models fallback for OpenAI-compat
// platforms as []openai.Model, synthesized from the unified servable set
// (servableIDs) and preferring the canonical openai.DefaultModels entry for an id
// when present (DisplayName/Created fidelity), else synthesizing — mirroring
// writeOpenAIModelsList. Converges with /pricing (drops advertised_dead like
// gpt-5-pro/gpt-image-*; surfaces every servable allowlist id).
func (h *GatewayHandler) tkOpenAIDefaultModelIDs(ctx context.Context, platform string) []openai.Model {
	return openai.ModelsForIDs(h.servableIDs(ctx, platform))
}

// tkClaudeDefaultModelIDs returns the /v1/models fallback for Claude as
// []claude.Model, synthesized from the unified servable set. The allowlist carries
// BASE ids (claude-opus-4-5) while claude.DefaultModels carries the canonical (often
// DATED) form (claude-opus-4-5-20251101); we index DefaultModels by their
// denormalized (base) id so a base servable id reuses the canonical entry —
// preserving the dated /v1/models wire form + DisplayName — and synthesize for
// allowlist-only ids absent from DefaultModels (e.g. claude-opus-4-1).
func (h *GatewayHandler) tkClaudeDefaultModelIDs(ctx context.Context, platform string) []claude.Model {
	return claude.ModelsForIDs(h.servableIDs(ctx, platform))
}

// tkAntigravityDefaultModels returns the /antigravity/models fallback as
// []antigravity.ClaudeModel, synthesized from the unified Antigravity servable
// set (Gemini + the PR #1265 live Claude subset; gpt-oss excluded).
// Preserves the []ClaudeModel shape (R-001) and prefers the canonical
// antigravity.DefaultModels() entry for DisplayName fidelity. The group
// supported_model_scopes filter still runs after this in AntigravityModels.
func (h *GatewayHandler) tkAntigravityDefaultModels(ctx context.Context) []antigravity.ClaudeModel {
	defaults := antigravity.DefaultModels()
	byID := make(map[string]antigravity.ClaudeModel, len(defaults))
	for _, m := range defaults {
		byID[m.ID] = m
	}
	ids := h.servableIDs(ctx, service.PlatformAntigravity)
	out := make([]antigravity.ClaudeModel, 0, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out = append(out, m)
			continue
		}
		out = append(out, antigravity.ClaudeModel{ID: id, Type: "model", DisplayName: id})
	}
	return out
}

// tkGeminiDefaultModelsList returns the /v1/models fallback for the gemini platform
// as []geminicli.Model, synthesized from the unified servable set so the gemini
// /v1/models list converges with /pricing (drops advertised_dead like
// gemini-2.0-flash). Prefers the canonical geminicli.DefaultModels entry for
// DisplayName fidelity.
func (h *GatewayHandler) tkGeminiDefaultModelsList(ctx context.Context) []geminicli.Model {
	return geminicli.ModelsForIDs(h.servableIDs(ctx, service.PlatformGemini))
}

// tkGeminiFallbackModelsList returns the /v1beta/models fallback as a
// gemini.ModelsListResponse, synthesized from the unified servable set. gemini.Model
// uses the "models/<id>" Name prefix; we restore it on output and prefer the
// canonical gemini.DefaultModels() entry (carrying SupportedGenerationMethods) when
// present, synthesizing a bare Name otherwise (a servable media id is advertised
// faithfully — without claiming generateContent it owns).
//
// Used by GeminiV1BetaListModels fallback paths (review finding CF-001).
func (h *GatewayHandler) tkGeminiFallbackModelsList(ctx context.Context) gemini.ModelsListResponse {
	defaults := gemini.DefaultModels()
	byID := make(map[string]gemini.Model, len(defaults))
	for _, m := range defaults {
		byID[strings.TrimPrefix(m.Name, "models/")] = m
	}
	ids := h.servableIDs(ctx, service.PlatformGemini)
	out := make([]gemini.Model, 0, len(ids))
	for _, id := range ids {
		if m, ok := byID[id]; ok {
			out = append(out, m)
			continue
		}
		out = append(out, gemini.Model{Name: "models/" + id})
	}
	return gemini.ModelsListResponse{Models: out}
}

// antigravityModelScope classifies an antigravity model id into the group
// supported_model_scopes vocabulary ("claude" / "gemini_text" / "gemini_image").
// gpt-oss gets its own bucket so a group without its scope filters it out (it is
// neither a gemini text nor image scope). Same bucketing as the frontend
// SubscriptionPlanCard badge labels (claude / gemini_text→Gemini / gemini_image→
// Imagen). Note the UseKeyModal usage guide does NOT classify per-model — it only
// gates the claude *flavor* on scopes.includes('claude'); the gemini_text vs
// gemini_image split is enforced here on /antigravity/v1/models only.
func antigravityModelScope(id string) string {
	l := strings.ToLower(strings.TrimSpace(id))
	switch {
	case strings.HasPrefix(l, "claude-"):
		return "claude"
	case strings.HasPrefix(l, "gpt-oss"):
		return "gpt_oss"
	case strings.Contains(l, "image"):
		return "gemini_image"
	default:
		return "gemini_text"
	}
}

// tkAntigravityFilterModelsByGroupScopes keeps only models whose scope is in the
// group's supported_model_scopes. Empty scopes = no restriction (back-compat for
// pre-#774 groups). The explicit account model_mapping ops apply flow can
// converge active Antigravity groups to [claude, gemini_text, gemini_image],
// while legacy/narrow groups can still intentionally hide Claude from
// /antigravity/v1/models until an operator applies that change.
func tkAntigravityFilterModelsByGroupScopes(scopes []string, models []antigravity.ClaudeModel) []antigravity.ClaudeModel {
	if len(scopes) == 0 {
		return models
	}
	allow := make(map[string]bool, len(scopes))
	for _, s := range scopes {
		allow[strings.TrimSpace(s)] = true
	}
	out := make([]antigravity.ClaudeModel, 0, len(models))
	for _, m := range models {
		if allow[antigravityModelScope(m.ID)] {
			out = append(out, m)
		}
	}
	return out
}
