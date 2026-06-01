package service

// TK media (image/video) pricing overlay.
//
// Why this exists: the production runtime price source (Wei-Shaw/model-price-repo)
// is a TRIMMED mirror of litellm — it drops provider-prefixed keys
// ("vertex_ai/imagen-4.0-generate-001") and token-less media entries entirely, so
// imagen-*/veo-* resolve to nothing and fall back to a wrong default (imagen) or $0
// (veo). litellm DOES carry these prices, but only under provider-prefixed keys, while
// the lookup path normalizes toward bare names and never reconstructs a prefix. Rather
// than open a second litellm sync pipeline (Wei-Shaw is already litellm-rooted — that
// would be same-source redundancy), TokenKey owns this tiny curated overlay of the
// handful of media models the mirror drops.
//
// Semantics: merged into every parsePricingData result (remote OR disk fallback) so the
// entries are present regardless of source. fill-only — the loaded source is authoritative
// and is never overwritten, so the overlay is self-deprecating: the day the source carries
// a bare media key natively, the source value wins and the entry here is ignored. The
// DB-backed ModelPricing override (model_pricing_resolver.go) still sits above everything.
//
// Price = vertex_ai provider (TK media traffic routes through Vertex ch41); see the JSON
// _meta block for provenance. Adding a media model = one JSON line; if media models ever
// proliferate, replace this with a provider-aware sync.

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

//go:embed tk_media_pricing_overlay.json
var tkMediaPricingOverlayRaw []byte

var (
	tkMediaPricingOverlayOnce sync.Once
	tkMediaPricingOverlay     map[string]*LiteLLMModelPricing
)

// loadTKMediaPricingOverlay parses the embedded overlay once. It deliberately does NOT
// call parsePricingData (that would recurse, since applyTKMediaPricingOverlay is invoked
// from inside parsePricingData) — it parses the small fixed file directly. Keys starting
// with "_" (e.g. "_meta") are provenance, not pricing, and are skipped.
func loadTKMediaPricingOverlay() map[string]*LiteLLMModelPricing {
	tkMediaPricingOverlayOnce.Do(func() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(tkMediaPricingOverlayRaw, &raw); err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] TK media overlay parse failed: %v", err)
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
			p := &LiteLLMModelPricing{LiteLLMProvider: e.LiteLLMProvider, Mode: e.Mode}
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
			out[name] = p
		}
		tkMediaPricingOverlay = out
	})
	return tkMediaPricingOverlay
}

// applyTKMediaPricingOverlay fills in TK-owned media pricing for models the loaded source
// does not already carry. fill-only by design (see file header).
func applyTKMediaPricingOverlay(result map[string]*LiteLLMModelPricing) {
	if result == nil {
		return
	}
	for name, pricing := range loadTKMediaPricingOverlay() {
		if _, ok := result[name]; ok {
			continue
		}
		result[name] = pricing
	}
}
