package service

// TK pricing overlay for models the trimmed runtime price source lacks.
//
// Why this exists: the production runtime price source (Wei-Shaw/model-price-repo)
// is a TRIMMED mirror of litellm — it drops provider-prefixed keys
// ("vertex_ai/imagen-4.0-generate-001") and token-less media entries entirely, so
// imagen-*/veo-* resolve to nothing and fall back to a wrong default (imagen) or $0
// (veo). litellm DOES carry these prices, but only under provider-prefixed keys, while
// the lookup path normalizes toward bare names and never reconstructs a prefix. Rather
// than open a second litellm sync pipeline (Wei-Shaw is already litellm-rooted — that
// would be same-source redundancy), TokenKey owns this tiny curated overlay of the
// handful of models the mirror drops.
//
// Scope (originally media-only, generalized 2026-06): any model the source lacks —
// media (imagen-*/veo-*, priced per-image/per-second) AND text models that litellm
// itself has not yet catalogued (e.g. deepseek-v4-*, which billed $0 in prod via
// "pricing_missing_record_zero_cost" until channel pricing was hand-configured).
// fill-only cannot fix WRONG source prices (e.g. deepseek-chat still carried at the
// pre-V4 rate): for those, use channel pricing (DB) — it overrides everything.
//
// Semantics: merged into every parsePricingData result (remote OR disk fallback) so the
// entries are present regardless of source. fill-only — the loaded source is authoritative
// and is never overwritten, so the overlay is self-deprecating: the day the source carries
// a bare key natively, the source value wins and the entry here is ignored. The
// DB-backed ModelPricing override (model_pricing_resolver.go) still sits above everything.
//
// Media prices = vertex_ai provider (TK media traffic routes through Vertex ch41); text
// prices = the provider's official list price. See the JSON _meta block for provenance.
// Adding a model = one JSON line; if entries ever proliferate, replace this with a
// provider-aware sync.

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

//go:embed tk_pricing_overlay.json
var tkPricingOverlayRaw []byte

var (
	tkPricingOverlayOnce sync.Once
	tkPricingOverlay     map[string]*LiteLLMModelPricing
)

// loadTKPricingOverlay parses the embedded overlay once. It deliberately does NOT
// call parsePricingData (that would recurse, since applyTKPricingOverlay is invoked
// from inside parsePricingData) — it parses the small fixed file directly. Keys starting
// with "_" (e.g. "_meta") are provenance, not pricing, and are skipped.
func loadTKPricingOverlay() map[string]*LiteLLMModelPricing {
	tkPricingOverlayOnce.Do(func() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(tkPricingOverlayRaw, &raw); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] TK pricing overlay parse failed: %v", err)
			return
		}
		out := make(map[string]*LiteLLMModelPricing, len(raw))
		for name, rawEntry := range raw {
			if strings.HasPrefix(name, "_") {
				continue
			}
			var e LiteLLMRawEntry
			if err := json.Unmarshal(rawEntry, &e); err != nil {
				continue
			}
			p := &LiteLLMModelPricing{
				LiteLLMProvider:       e.LiteLLMProvider,
				Mode:                  e.Mode,
				SupportsPromptCaching: e.SupportsPromptCaching,
			}
			if e.OutputCostPerImage != nil {
				p.OutputCostPerImage = *e.OutputCostPerImage
			}
			if e.OutputCostPerImageToken != nil {
				p.OutputCostPerImageToken = *e.OutputCostPerImageToken
			}
			if e.OutputCostPerSecond != nil {
				p.OutputCostPerSecond = *e.OutputCostPerSecond
			}
			if e.InputCostPerToken != nil {
				p.InputCostPerToken = *e.InputCostPerToken
			}
			if e.OutputCostPerToken != nil {
				p.OutputCostPerToken = *e.OutputCostPerToken
			}
			if e.CacheCreationInputTokenCost != nil {
				p.CacheCreationInputTokenCost = *e.CacheCreationInputTokenCost
			}
			if e.CacheCreationInputTokenCostAbove1hr != nil {
				p.CacheCreationInputTokenCostAbove1hr = *e.CacheCreationInputTokenCostAbove1hr
			}
			if e.CacheReadInputTokenCost != nil {
				p.CacheReadInputTokenCost = *e.CacheReadInputTokenCost
			}
			out[name] = p
		}
		tkPricingOverlay = out
	})
	return tkPricingOverlay
}

// applyTKPricingOverlay fills in TK-owned pricing for models the loaded source
// does not already carry. fill-only by design (see file header).
func applyTKPricingOverlay(result map[string]*LiteLLMModelPricing) {
	if result == nil {
		return
	}
	for name, pricing := range loadTKPricingOverlay() {
		if _, ok := result[name]; ok {
			continue
		}
		result[name] = pricing
	}
}
