package service

// TK pricing registry (the filename keeps the historical "overlay" suffix for
// compatibility). This embedded snapshot is the sole runtime owner for
// base prices, catalog metadata, and serving-price gates across native platforms
// and active newapi channels. A scoped channel_model_pricing row may override it
// at resolution time; provider/LiteLLM data is import evidence only.
//
// Runtime loading always starts from this immutable registry. The flexible
// LiteLLM-shaped parser remains only for registry validation, offline import
// tooling, and parser tests, so no external source can become a second runtime
// owner.

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
	OfficialListBaseTax   *tkOfficialListBaseTaxPolicy `json:"official_list_base_tax"`
	DeepSeekPeakValley    *tkDeepSeekPeakValleyPolicy  `json:"deepseek_peak_valley"`
	WebSearchPricePerCall *float64                     `json:"web_search_price_per_call"`
}

type tkPricingOverlaySnapshot struct {
	Models                map[string]*LiteLLMModelPricing
	BaseTax               tkOfficialListBaseTaxPolicy
	DeepSeekPeakValley    *tkDeepSeekPeakValleyPolicy
	WebSearchPricePerCall float64
}

type tkPricingOverlayDocument struct {
	Models                map[string]*LiteLLMModelPricing
	BaseTax               *tkOfficialListBaseTaxPolicy
	DeepSeekPeakValley    *tkDeepSeekPeakValleyPolicy
	WebSearchPricePerCall *float64
}

// The embedded registry is parsed once per process. A release is the only way to
// replace this global snapshot; channel_model_pricing remains the separate,
// explicitly scoped override resolved above it.
var (
	tkPricingRegistryOnce     sync.Once
	tkPricingRegistrySnapshot *tkPricingOverlaySnapshot
)

// parseTKOverlayDocument parses the registry-shaped JSON used by the embedded
// owner and offline import validation. _config is strict when present.
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
		if config.WebSearchPricePerCall != nil {
			if *config.WebSearchPricePerCall < 0 || !finiteNonNegative(*config.WebSearchPricePerCall) {
				return nil, fmt.Errorf("overlay _config.web_search_price_per_call must be finite and >= 0")
			}
			price := *config.WebSearchPricePerCall
			doc.WebSearchPricePerCall = &price
		}
	}

	for name, rawEntry := range raw {
		if strings.HasPrefix(name, "_") {
			continue
		}
		if name != strings.ToLower(strings.TrimSpace(name)) || strings.Contains(name, "/") {
			return nil, fmt.Errorf("overlay model owner %q must be a normalized bare key", name)
		}
		p, _, err := tkParseRegistryPricingEntry(name, rawEntry)
		if err != nil {
			return nil, err
		}
		doc.Models[name] = p
	}
	return doc, nil
}

// tkParseRegistryPricingEntry maps ONE registry row into its runtime pricing
// struct. It is the single projection used by every consumer of the registry
// schema — the production loader (parseTKOverlayDocument) and the offline
// import/validation parser (PricingService.parsePricingData) — so no caller can
// observe a different set of fields than billing does.
//
// Before this was shared, the two parsers diverged silently: the offline one
// dropped `intervals` and `video_price_tiers` entirely and duplicated the 272K
// long-context normalization, which meant guard/contract tests built on it were
// asserting against a shape production never produced.
//
// Returns the mapped row plus the decoded raw entry, whose pointer fields let a
// caller distinguish "absent" from "explicit zero" without re-unmarshalling.
func tkParseRegistryPricingEntry(name string, rawEntry json.RawMessage) (*LiteLLMModelPricing, *LiteLLMRawEntry, error) {
	var e LiteLLMRawEntry
	if err := json.Unmarshal(rawEntry, &e); err != nil {
		return nil, nil, fmt.Errorf("parse overlay model %s: %w", name, err)
	}
	p := &LiteLLMModelPricing{
		LiteLLMProvider:         e.LiteLLMProvider,
		Mode:                    e.Mode,
		SupportsPromptCaching:   e.SupportsPromptCaching,
		SupportsServiceTier:     e.SupportsServiceTier,
		MaxInputTokens:          e.MaxInputTokens,
		MaxOutputTokens:         e.MaxOutputTokens,
		SupportsVision:          e.SupportsVision,
		SupportsToolChoice:      e.SupportsToolChoice,
		SupportsFunctionCalling: e.SupportsFunctionCalling,
		SupportsReasoning:       e.SupportsReasoning,
		SupportsResponseSchema:  e.SupportsResponseSchema,
		SupportsPDFInput:        e.SupportsPDFInput,
		SupportsWebSearch:       e.SupportsWebSearch,
		TokenPricingAbsent:      e.InputCostPerToken == nil && e.OutputCostPerToken == nil && e.InputCostPerImageToken == nil,
		ExplicitFree:            e.ExplicitFree,
	}
	if e.OutputCostPerImage != nil {
		p.OutputCostPerImage = *e.OutputCostPerImage
	}
	if e.OutputCostPerImageToken != nil {
		p.OutputCostPerImageToken = *e.OutputCostPerImageToken
	}
	if e.InputCostPerImageToken != nil {
		p.InputCostPerImageToken = *e.InputCostPerImageToken
	}
	if e.ImagePrice1K != nil {
		p.ImagePrice1K = *e.ImagePrice1K
	}
	if e.ImagePrice2K != nil {
		p.ImagePrice2K = *e.ImagePrice2K
	}
	if e.ImagePrice4K != nil {
		p.ImagePrice4K = *e.ImagePrice4K
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
	if e.LongContextInputCostMultiplier != nil {
		p.LongContextInputCostMultiplier = *e.LongContextInputCostMultiplier
	}
	if e.LongContextOutputCostMultiplier != nil {
		p.LongContextOutputCostMultiplier = *e.LongContextOutputCostMultiplier
	}
	if e.LongContextInputTokenThreshold != nil {
		p.LongContextInputTokenThreshold = *e.LongContextInputTokenThreshold
	} else if e.InputCostPerTokenAbove272K != nil || e.OutputCostPerTokenAbove272K != nil || e.CacheReadInputTokenCostAbove272K != nil {
		p.LongContextInputTokenThreshold = 272000
	}
	if p.LongContextInputCostMultiplier == 0 && e.InputCostPerToken != nil && e.InputCostPerTokenAbove272K != nil && *e.InputCostPerToken > 0 {
		p.LongContextInputCostMultiplier = *e.InputCostPerTokenAbove272K / *e.InputCostPerToken
	}
	if p.LongContextOutputCostMultiplier == 0 && e.OutputCostPerToken != nil && e.OutputCostPerTokenAbove272K != nil && *e.OutputCostPerToken > 0 {
		p.LongContextOutputCostMultiplier = *e.OutputCostPerTokenAbove272K / *e.OutputCostPerToken
	}
	// TK: input-token interval (tiered) pricing. LiteLLMRawEntry has no
	// "intervals" field (it is TK-overlay-only), so parse the raw entry a
	// second time into a TK-local shape. An entry's flat input/output cost
	// stays as the out-of-range fallback (BasePricing); the intervals drive
	// whole-request tier billing via ResolvedPricing.Intervals.
	var ext struct {
		Intervals []tkOverlayRawInterval `json:"intervals"`
	}
	if err := json.Unmarshal(rawEntry, &ext); err != nil {
		return nil, nil, fmt.Errorf("parse overlay model %s intervals: %w", name, err)
	}
	if len(ext.Intervals) > 0 {
		p.Intervals = tkBuildOverlayIntervals(ext.Intervals)
		if err := ValidateIntervals(p.Intervals, BillingModeToken); err != nil {
			return nil, nil, fmt.Errorf("overlay model %s intervals: %w", name, err)
		}
	}
	var videoExt struct {
		VideoPriceTiers        []tkOverlayRawVideoTier `json:"video_price_tiers"`
		DefaultVideoResolution string                  `json:"default_video_resolution"`
	}
	if err := json.Unmarshal(rawEntry, &videoExt); err != nil {
		return nil, nil, fmt.Errorf("parse overlay model %s video tiers: %w", name, err)
	}
	if videoExt.VideoPriceTiers != nil {
		tiers, defaultResolution, err := tkValidateAndBuildOverlayVideoTiers(
			videoExt.VideoPriceTiers,
			videoExt.DefaultVideoResolution,
			p.OutputCostPerSecond,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("overlay model %s video tiers: %w", name, err)
		}
		p.VideoPriceTiers = tiers
		p.DefaultVideoResolution = defaultResolution
	} else if strings.TrimSpace(videoExt.DefaultVideoResolution) != "" {
		return nil, nil, fmt.Errorf("overlay model %s has default_video_resolution without video_price_tiers", name)
	}
	return p, &e, nil
}

func buildTKPricingOverlaySnapshot() (*tkPricingOverlaySnapshot, error) {
	base, err := parseTKOverlayDocument(tkPricingOverlayRaw)
	if err != nil {
		return nil, fmt.Errorf("parse embedded TK pricing registry: %w", err)
	}
	if base.BaseTax == nil {
		return nil, fmt.Errorf("embedded TK pricing registry missing _config.official_list_base_tax")
	}
	if len(base.Models) == 0 {
		return nil, fmt.Errorf("embedded TK pricing registry has no model owners")
	}
	snapshot := &tkPricingOverlaySnapshot{Models: base.Models, BaseTax: *base.BaseTax}
	if base.DeepSeekPeakValley != nil {
		policy := *base.DeepSeekPeakValley
		snapshot.DeepSeekPeakValley = &policy
	}
	if base.WebSearchPricePerCall != nil {
		snapshot.WebSearchPricePerCall = *base.WebSearchPricePerCall
	}
	return snapshot, nil
}

func loadTKPricingOverlaySnapshot() *tkPricingOverlaySnapshot {
	tkPricingRegistryOnce.Do(func() {
		snapshot, err := buildTKPricingOverlaySnapshot()
		if err != nil {
			logger.LegacyPrintf("service.pricing", "[Pricing] TK pricing registry build failed: %v", err)
			return
		}
		tkPricingRegistrySnapshot = snapshot
	})
	return tkPricingRegistrySnapshot
}

// loadTKPricingOverlay returns the process-wide immutable registry owner map.
func loadTKPricingOverlay() map[string]*LiteLLMModelPricing {
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil {
		return nil
	}
	return snapshot.Models
}

func tkRegistryWebSearchPricePerCall() float64 {
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil {
		return 0
	}
	return snapshot.WebSearchPricePerCall
}

// tkOverlayLiteLLMModelPricing returns the immutable overlay row in the
// LiteLLM-shaped form consumed by PricingService. The caller applies the shared
// presentation tax/clone policy without consulting another data source.
func tkOverlayLiteLLMModelPricing(model string) *LiteLLMModelPricing {
	row := loadTKPricingOverlay()[strings.ToLower(strings.TrimSpace(model))]
	if row == nil {
		return nil
	}
	copy := *row
	return &copy
}

// applyTKPricingOverlay is retained for offline provider-import tests. Runtime
// loading starts from the registry snapshot and never merges an external source.
func applyTKPricingOverlay(result map[string]*LiteLLMModelPricing) {
	if result == nil {
		return
	}
	for name, pricing := range loadTKPricingOverlay() {
		// The imported map is evidence only. A registry row always wins for the
		// same model; imports may contribute only models not yet registry-owned.
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
	if p.ExplicitFree {
		return false
	}
	// Interval (tiered) pricing is a price even if the flat base fields were left
	// zero — never treat a tiered overlay entry as a placeholder.
	if len(p.Intervals) > 0 {
		return false
	}
	if len(p.VideoPriceTiers) > 0 {
		return false
	}
	return p.InputCostPerToken == 0 &&
		p.InputCostPerImageToken == 0 &&
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

// tkOverlayRawVideoTier is the JSON shape of one video_price_tiers[] row in overlay.
type tkOverlayRawVideoTier struct {
	Resolution                   string   `json:"resolution"`
	OutputCostPerSecond          *float64 `json:"output_cost_per_second"`
	OutputCostPerSecondSilent    *float64 `json:"output_cost_per_second_silent"`
	InputImageSurchargePerSecond *float64 `json:"input_image_surcharge_per_second"`
	DefaultForModel              bool     `json:"default_for_model"`
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
