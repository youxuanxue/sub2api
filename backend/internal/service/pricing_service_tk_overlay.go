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
// fill-only cannot fix WRONG NON-ZERO source prices (e.g. deepseek-chat still carried
// at the pre-V4 rate): those are a judgment call between two claimed prices — use
// channel pricing (DB), it overrides everything.
//
// Semantics: merged into every parsePricingData result (remote OR disk fallback) so the
// entries are present regardless of source. Fill applies when the source key is ABSENT
// or the source entry is EFFECTIVELY UNPRICED (every cost field zero — see
// tkIsEffectivelyUnpriced). A zero-priced entry is not a price, it is the absence of a
// price wearing a price's shape: litellm marks unknown costs 0.0 (e.g.
// deepseek-v3-2-251201 under volcengine), which billed $0 in prod for weeks with no
// alert because the key LOOKED present. The source stays authoritative for any entry
// carrying a real (non-zero) cost, so the overlay remains self-deprecating: the day the
// source carries a real price for a bare key, the source value wins and the entry here
// is ignored. The DB-backed ModelPricing override (model_pricing_resolver.go) still
// sits above everything.
//
// Media prices = vertex_ai provider (TK media traffic routes through Vertex ch41); text
// prices = the provider's official list price. See the JSON _meta block for provenance.
// Adding a model = one JSON line; if entries ever proliferate, replace this with a
// provider-aware sync.

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

//go:embed tk_pricing_overlay.json
var tkPricingOverlayRaw []byte

type tkPricingOverlayExecutableConfig struct {
	OfficialListBaseTax  *tkOfficialListBaseTaxPolicy  `json:"official_list_base_tax"`
	DeepSeekPeakValley   *tkDeepSeekPeakValleyPolicy   `json:"deepseek_peak_valley"`
}

type tkPricingOverlaySnapshot struct {
	Models             map[string]*LiteLLMModelPricing
	BaseTax            tkOfficialListBaseTaxPolicy
	DeepSeekPeakValley *tkDeepSeekPeakValleyPolicy
}

type tkPricingOverlayDocument struct {
	Models             map[string]*LiteLLMModelPricing
	BaseTax            *tkOfficialListBaseTaxPolicy
	DeepSeekPeakValley *tkDeepSeekPeakValleyPolicy
}

// tkOverlayEffective is the live immutable snapshot = embedded ∪ runtime-settings
// (runtime wins on model conflicts and may replace executable policy). Model prices
// and tax policy swap under one lock so billing, /pricing, and fallback classification
// never read policy from a different runtime generation.
var (
	tkOverlayMu        sync.RWMutex
	tkOverlayEffective *tkPricingOverlaySnapshot
)

// parseTKOverlayDocument parses an overlay JSON object (the embedded file OR the
// runtime settings blob) into model prices plus optional executable configuration.
// Runtime blobs may omit _config and inherit the embedded policy; when present,
// _config is strict and invalid policy rejects the whole runtime swap.
func parseTKOverlayDocument(data []byte) (*tkPricingOverlayDocument, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	doc := &tkPricingOverlayDocument{Models: make(map[string]*LiteLLMModelPricing, len(raw))}
	if rawConfig, ok := raw["_config"]; ok {
		var config tkPricingOverlayExecutableConfig
		decoder := json.NewDecoder(bytes.NewReader(rawConfig))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return nil, fmt.Errorf("parse overlay _config: %w", err)
		}
		if config.OfficialListBaseTax == nil {
			return nil, fmt.Errorf("overlay _config.official_list_base_tax is required")
		}
		if err := config.OfficialListBaseTax.validate(); err != nil {
			return nil, err
		}
		policy := *config.OfficialListBaseTax
		doc.BaseTax = &policy
		if config.DeepSeekPeakValley != nil {
			if err := config.DeepSeekPeakValley.validate(); err != nil {
				return nil, fmt.Errorf("overlay _config.deepseek_peak_valley: %w", err)
			}
			peakPolicy := *config.DeepSeekPeakValley
			doc.DeepSeekPeakValley = &peakPolicy
		}
	}

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
			SupportsServiceTier:   e.SupportsServiceTier,
			TokenPricingAbsent:    e.InputCostPerToken == nil && e.OutputCostPerToken == nil,
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
		if e.InputCostPerTokenPriority != nil {
			p.InputCostPerTokenPriority = *e.InputCostPerTokenPriority
		}
		if e.OutputCostPerToken != nil {
			p.OutputCostPerToken = *e.OutputCostPerToken
		}
		if e.OutputCostPerTokenPriority != nil {
			p.OutputCostPerTokenPriority = *e.OutputCostPerTokenPriority
		}
		if e.ThinkingOutputCostPerToken != nil {
			p.ThinkingOutputCostPerToken = *e.ThinkingOutputCostPerToken
		}
		if e.CacheCreationInputTokenCost != nil {
			p.CacheCreationInputTokenCost = *e.CacheCreationInputTokenCost
		}
		if e.CacheCreationInputTokenCostPriority != nil {
			p.CacheCreationInputTokenCostPriority = *e.CacheCreationInputTokenCostPriority
		}
		if e.CacheCreationInputTokenCostAbove1hr != nil {
			p.CacheCreationInputTokenCostAbove1hr = *e.CacheCreationInputTokenCostAbove1hr
		}
		if e.CacheReadInputTokenCost != nil {
			p.CacheReadInputTokenCost = *e.CacheReadInputTokenCost
		}
		if e.CacheReadInputTokenCostPriority != nil {
			p.CacheReadInputTokenCostPriority = *e.CacheReadInputTokenCostPriority
		}
		if e.LongContextInputTokenThreshold != nil {
			p.LongContextInputTokenThreshold = *e.LongContextInputTokenThreshold
		}
		if e.LongContextInputCostMultiplier != nil {
			p.LongContextInputCostMultiplier = *e.LongContextInputCostMultiplier
		}
		if e.LongContextOutputCostMultiplier != nil {
			p.LongContextOutputCostMultiplier = *e.LongContextOutputCostMultiplier
		}
		// TK: input-token interval (tiered) pricing. LiteLLMRawEntry has no
		// "intervals" field (it is TK-overlay-only), so parse the raw entry a
		// second time into a TK-local shape. An entry's flat input/output cost
		// stays as the out-of-range fallback (BasePricing); the intervals drive
		// whole-request tier billing via ResolvedPricing.Intervals.
		var ext struct {
			Intervals []tkOverlayRawInterval `json:"intervals"`
		}
		if err := json.Unmarshal(rawEntry, &ext); err == nil && len(ext.Intervals) > 0 {
			p.Intervals = tkBuildOverlayIntervals(ext.Intervals)
		}
		doc.Models[name] = p
	}
	return doc, nil
}

func validateRuntimeBaseTaxCoverage(embedded, runtime tkOfficialListBaseTaxPolicy) error {
	runtimeProviders := make(map[string]struct{}, len(runtime.Rules))
	for _, rule := range runtime.Rules {
		runtimeProviders[rule.Provider] = struct{}{}
	}
	var missing []string
	for _, rule := range embedded.Rules {
		if _, ok := runtimeProviders[rule.Provider]; !ok {
			missing = append(missing, rule.Provider)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("runtime official_list_base_tax drops embedded providers: %s", strings.Join(missing, ", "))
	}
	return nil
}

func buildTKPricingOverlaySnapshot(runtimeBytes []byte) (*tkPricingOverlaySnapshot, error) {
	base, err := parseTKOverlayDocument(tkPricingOverlayRaw)
	if err != nil {
		return nil, fmt.Errorf("parse embedded TK overlay: %w", err)
	}
	if base.BaseTax == nil {
		return nil, fmt.Errorf("embedded TK overlay missing _config.official_list_base_tax")
	}
	if len(runtimeBytes) > 0 {
		runtime, err := parseTKOverlayDocument(runtimeBytes)
		if err != nil {
			return nil, fmt.Errorf("parse runtime TK overlay: %w", err)
		}
		for k, v := range runtime.Models {
			base.Models[k] = v
		}
		if runtime.BaseTax != nil {
			if err := validateRuntimeBaseTaxCoverage(*base.BaseTax, *runtime.BaseTax); err != nil {
				return nil, err
			}
			base.BaseTax = runtime.BaseTax
		}
		if runtime.DeepSeekPeakValley != nil {
			base.DeepSeekPeakValley = runtime.DeepSeekPeakValley
		}
	}
	snapshot := &tkPricingOverlaySnapshot{Models: base.Models, BaseTax: *base.BaseTax}
	if base.DeepSeekPeakValley != nil {
		policy := *base.DeepSeekPeakValley
		snapshot.DeepSeekPeakValley = &policy
	}
	return snapshot, nil
}

// rebuildTKOverlayUnion recomputes the effective overlay = embedded ∪ runtime
// (runtime wins on key conflict) and atomically swaps it under tkOverlayMu.
//
// Safety invariants (never serve $0):
//   - The embedded JSON is parsed fresh each call as the FLOOR. If the embedded
//     itself fails to parse (should be impossible — it is gated by
//     pricing-overlay.py), the previous effective map is KEPT, not blanked.
//   - A nil / empty runtimeBytes yields embedded-only. An invalid runtime keeps
//     the previous snapshot; on first load it still establishes the embedded
//     floor, so a corrupt setting can never leave the effective map empty.
func rebuildTKOverlayUnion(runtimeBytes []byte) {
	snapshot, err := buildTKPricingOverlaySnapshot(runtimeBytes)
	if err != nil {
		tkOverlayMu.RLock()
		hasCurrent := tkOverlayEffective != nil
		tkOverlayMu.RUnlock()
		if !hasCurrent {
			floor, floorErr := buildTKPricingOverlaySnapshot(nil)
			if floorErr == nil {
				tkOverlayMu.Lock()
				if tkOverlayEffective == nil {
					tkOverlayEffective = floor
				}
				tkOverlayMu.Unlock()
			}
		}
		// Invalid runtime keeps the previous immutable snapshot. If this is the
		// first load, the embedded-only build above establishes the pricing floor.
		logger.LegacyPrintf("service.pricing", "[Pricing] TK overlay snapshot build failed (keeping current effective map): %v", err)
		return
	}
	tkOverlayMu.Lock()
	tkOverlayEffective = snapshot
	tkOverlayMu.Unlock()
}

func loadTKPricingOverlaySnapshot() *tkPricingOverlaySnapshot {
	tkOverlayMu.RLock()
	snapshot := tkOverlayEffective
	tkOverlayMu.RUnlock()
	if snapshot != nil {
		return snapshot
	}
	rebuildTKOverlayUnion(nil)
	tkOverlayMu.RLock()
	defer tkOverlayMu.RUnlock()
	return tkOverlayEffective
}

// loadTKPricingOverlay returns the live effective overlay (embedded ∪ runtime).
// First call before any explicit rebuild lazily builds the embedded-only floor,
// so a process that never loads a runtime blob behaves exactly as before.
func loadTKPricingOverlay() map[string]*LiteLLMModelPricing {
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil {
		return nil
	}
	return snapshot.Models
}

// tkOverlayOverridesLitellmSource reports whether a TK overlay row is the
// authoritative official list price and must replace a non-zero litellm-mirror
// entry. GLM chat models use BigModel.cn as the sole pricing source; the
// litellm mirror often carries stale USD guesses (e.g. glm-5.2 at $1.4/$4.4 per
// Mtok) that must not win over the curated overlay.
func tkOverlayOverridesLitellmSource(modelID string, overlay *LiteLLMModelPricing) bool {
	if overlay == nil {
		return false
	}
	if strings.ToLower(strings.TrimSpace(overlay.LiteLLMProvider)) != "zhipu" {
		return false
	}
	m := strings.ToLower(strings.TrimSpace(modelID))
	if !strings.HasPrefix(m, "glm-") {
		return false
	}
	return isTkCuratedNewAPIModelListed(modelID)
}

// applyTKPricingOverlay fills in TK-owned pricing for models the loaded source
// does not already carry — or carries only as an effectively-unpriced (all-zero)
// placeholder. Real source prices are never overwritten (see file header),
// except for GLM rows where the overlay is the authoritative BigModel list price.
func applyTKPricingOverlay(result map[string]*LiteLLMModelPricing) {
	if result == nil {
		return
	}
	for name, pricing := range loadTKPricingOverlay() {
		existing, ok := result[name]
		if ok && !tkIsEffectivelyUnpriced(existing) && !tkOverlayOverridesLitellmSource(name, pricing) {
			continue
		}
		result[name] = pricing
	}
}

// tkIsEffectivelyUnpriced reports whether a pricing entry carries no billable
// price at all: every cost field is zero. litellm uses 0.0 for "cost unknown"
// (not "free"), so such an entry must not shadow a curated overlay price, and
// billing must not treat it as a successful pricing lookup. Entries priced only
// per-image / per-second (imagen, veo) have zero token costs but a non-zero
// media cost field, so they correctly count as priced.
func tkIsEffectivelyUnpriced(p *LiteLLMModelPricing) bool {
	if p == nil {
		return true
	}
	// Interval (tiered) pricing is a price even if the flat base fields were left
	// zero — never treat a tiered overlay entry as a placeholder.
	if len(p.Intervals) > 0 {
		return false
	}
	return p.InputCostPerToken == 0 &&
		p.InputCostPerTokenPriority == 0 &&
		p.OutputCostPerToken == 0 &&
		p.OutputCostPerTokenPriority == 0 &&
		p.CacheCreationInputTokenCost == 0 &&
		p.CacheCreationInputTokenCostAbove1hr == 0 &&
		p.CacheReadInputTokenCost == 0 &&
		p.CacheReadInputTokenCostPriority == 0 &&
		p.OutputCostPerImage == 0 &&
		p.OutputCostPerImageToken == 0 &&
		p.OutputCostPerSecond == 0
}

// tkOverlayRawInterval is the JSON shape of one entry in an overlay model's
// "intervals" array. Boundaries follow FindMatchingInterval (channel.go):
// MinTokens is EXCLUSIVE, MaxTokens INCLUSIVE (nil = unbounded), keyed on the
// request's input context tokens (InputTokens + CacheReadTokens) — exactly the
// DashScope "0<Token<=256K" tier semantics. Costs are USD per single token.
type tkOverlayRawInterval struct {
	MinTokens                   int      `json:"min_tokens"`
	MaxTokens                   *int     `json:"max_tokens"`
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
}

// tkBuildOverlayIntervals converts the parsed overlay intervals into the shared
// PricingInterval shape the billing engine already consumes (FindMatchingInterval
// + tkOverlayIntervalOntoBasePricing). SortOrder preserves the JSON order.
func tkBuildOverlayIntervals(raw []tkOverlayRawInterval) []PricingInterval {
	out := make([]PricingInterval, 0, len(raw))
	for i := range raw {
		r := raw[i]
		out = append(out, PricingInterval{
			MinTokens:       r.MinTokens,
			MaxTokens:       r.MaxTokens,
			InputPrice:      r.InputCostPerToken,
			OutputPrice:     r.OutputCostPerToken,
			CacheReadPrice:  r.CacheReadInputTokenCost,
			CacheWritePrice: r.CacheCreationInputTokenCost,
			SortOrder:       i,
		})
	}
	return out
}
