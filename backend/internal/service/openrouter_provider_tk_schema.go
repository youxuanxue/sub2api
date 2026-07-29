package service

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const (
	openRouterProviderDefaultQuantization = "fp16"
)

var openRouterProviderDefaultSamplingParameters = []string{
	"temperature",
	"top_p",
	"max_tokens",
	"stop",
}

type OpenRouterProviderPricingOverride struct {
	MinPromptTokens int    `json:"min_prompt_tokens,omitempty"`
	UTCStart        int    `json:"utc_start,omitempty"`
	UTCEnd          int    `json:"utc_end,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	Completion      string `json:"completion,omitempty"`
	InputCacheRead  string `json:"input_cache_read,omitempty"`
}

func openRouterProviderCatalogIndex(catalog *PublicCatalogResponse) map[string]PublicCatalogModel {
	out := make(map[string]PublicCatalogModel)
	if catalog == nil {
		return out
	}
	for _, item := range catalog.Data {
		id := strings.TrimSpace(item.ModelID)
		if id == "" {
			continue
		}
		out[id] = item
	}
	return out
}

func openRouterProviderInputModalities(meta *PublicCatalogModel) []string {
	modalities := []string{"text"}
	if meta == nil {
		return modalities
	}
	for _, cap := range meta.Capabilities {
		switch cap {
		case "vision":
			modalities = openRouterAppendUniqueString(modalities, "image")
		case "pdf_input":
			modalities = openRouterAppendUniqueString(modalities, "file")
		}
	}
	return modalities
}

func openRouterProviderOutputModalities(meta *PublicCatalogModel) []string {
	if meta == nil {
		return []string{"text"}
	}
	switch meta.Pricing.BillingMode {
	case "image":
		return []string{"image"}
	case "video":
		return []string{"video"}
	default:
		return []string{"text"}
	}
}

func openRouterProviderSupportedFeatures(meta *PublicCatalogModel) []string {
	if meta == nil || len(meta.Capabilities) == 0 {
		return nil
	}
	features := make([]string, 0, 6)
	for _, cap := range meta.Capabilities {
		switch cap {
		case "tool_use":
			features = openRouterAppendUniqueString(features, "tools")
		case "response_schema":
			features = openRouterAppendUniqueString(features, "json_mode")
			features = openRouterAppendUniqueString(features, "structured_outputs")
		case "reasoning":
			features = openRouterAppendUniqueString(features, "reasoning")
		case "web_search":
			features = openRouterAppendUniqueString(features, "web_search")
		}
	}
	if len(features) == 0 {
		return nil
	}
	return features
}

func openRouterProviderDescription(cfg OpenRouterProviderConfig, publicID string, meta *PublicCatalogModel) string {
	name := openRouterProviderDisplayName(cfg, publicID, meta)
	if strings.TrimSpace(name) == "" {
		return publicID
	}
	return name + " via TokenKey"
}

func openRouterProviderDisplayName(cfg OpenRouterProviderConfig, publicID string, meta *PublicCatalogModel) string {
	if meta != nil {
		vendor := strings.TrimSpace(meta.Vendor)
		if vendor != "" {
			return vendor + ": " + publicID
		}
	}
	slug := strings.TrimSpace(cfg.Slug)
	if slug != "" {
		return slug + ": " + publicID
	}
	return publicID
}

func openRouterProviderContextLength(cfg OpenRouterProviderConfig, meta *PublicCatalogModel) int {
	if meta != nil && meta.ContextWindow > 0 {
		return meta.ContextWindow
	}
	if cfg.DefaultContextLen > 0 {
		return cfg.DefaultContextLen
	}
	return DefaultOpenRouterProviderConfig().DefaultContextLen
}

func openRouterProviderMaxOutputLength(cfg OpenRouterProviderConfig, meta *PublicCatalogModel) int {
	if meta != nil && meta.MaxOutputTokens > 0 {
		return meta.MaxOutputTokens
	}
	ctxLen := openRouterProviderContextLength(cfg, meta)
	if ctxLen > 0 {
		return ctxLen / 4
	}
	return 8192
}

func openRouterProviderCacheReadUSDPerToken(meta *PublicCatalogModel) float64 {
	if meta == nil || meta.Pricing.CacheReadPer1K <= 0 {
		return 0
	}
	return meta.Pricing.CacheReadPer1K / 1000
}

// openRouterProviderModelHasListedPrice reports whether a candidate model should appear
// on GET /openrouter/v1/models. Token-priced models and media-priced models (per-image
// or per-second) both qualify; unpriced rows are omitted from the OR seller catalog.
func openRouterProviderModelHasListedPrice(meta *PublicCatalogModel, promptUSD, completionUSD float64) bool {
	if promptUSD > 0 || completionUSD > 0 {
		return true
	}
	return openRouterProviderMediaUnitUSD(meta) > 0
}

func openRouterProviderMediaUnitUSD(meta *PublicCatalogModel) float64 {
	if meta == nil {
		return 0
	}
	switch meta.Pricing.BillingMode {
	case "image":
		return meta.Pricing.OutputCostPerImage
	case "video":
		return meta.Pricing.OutputCostPerSecond
	}
	if meta.Pricing.OutputCostPerImage > 0 {
		return meta.Pricing.OutputCostPerImage
	}
	return meta.Pricing.OutputCostPerSecond
}

func openRouterProviderBaseMultiplier(group *Group, userMultiplier float64) float64 {
	mult := 1.0
	if group != nil && group.RateMultiplier > 0 {
		mult = group.RateMultiplier
	}
	if userMultiplier > 0 {
		mult *= userMultiplier
	}
	if mult <= 0 {
		return 1
	}
	return mult
}

func openRouterProviderBuildPricing(
	group *Group,
	meta *PublicCatalogModel,
	promptUSD, completionUSD, cacheReadUSD, baseMult float64,
) OpenRouterProviderModelPricing {
	pricing := OpenRouterProviderModelPricing{
		Prompt:         formatOpenRouterUSDPerToken(promptUSD * baseMult),
		Completion:     formatOpenRouterUSDPerToken(completionUSD * baseMult),
		Image:          "0",
		Request:        "0",
		InputCacheRead: formatOpenRouterUSDPerToken(cacheReadUSD * baseMult),
	}
	if meta != nil && meta.Pricing.BillingMode == "image" && meta.Pricing.OutputCostPerImage > 0 {
		pricing.Image = formatOpenRouterUSDPerToken(meta.Pricing.OutputCostPerImage * baseMult)
	}
	if meta != nil && meta.Pricing.BillingMode == "video" && meta.Pricing.OutputCostPerSecond > 0 {
		pricing.Request = formatOpenRouterUSDPerToken(meta.Pricing.OutputCostPerSecond * openRouterProviderDefaultVideoQuoteSeconds * baseMult)
	}
	if tierOverrides := openRouterProviderTierPricingOverrides(meta, baseMult); len(tierOverrides) > 0 {
		pricing.Overrides = tierOverrides
		return pricing
	}
	if group != nil && group.PeakRateEnabled && group.PeakRateMultiplier > 1 {
		if override, ok := openRouterProviderPeakPricingOverride(group, promptUSD, completionUSD, cacheReadUSD, baseMult); ok {
			pricing.Overrides = []OpenRouterProviderPricingOverride{override}
		}
	}
	return pricing
}

func openRouterProviderTierPricingOverrides(meta *PublicCatalogModel, baseMult float64) []OpenRouterProviderPricingOverride {
	if meta == nil || len(meta.Pricing.Tiers) <= 1 {
		return nil
	}
	out := make([]OpenRouterProviderPricingOverride, 0, 2)
	for i, tier := range meta.Pricing.Tiers {
		if i == 0 || tier.MinTokens <= 0 {
			continue
		}
		if len(out) >= 2 {
			break
		}
		cacheReadUSD := 0.0
		if tier.CacheReadPer1K > 0 {
			cacheReadUSD = tier.CacheReadPer1K / 1000
		}
		out = append(out, OpenRouterProviderPricingOverride{
			MinPromptTokens: tier.MinTokens,
			Prompt:          formatOpenRouterUSDPerToken(tier.InputPer1KTokens / 1000 * baseMult),
			Completion:      formatOpenRouterUSDPerToken(tier.OutputPer1KTokens / 1000 * baseMult),
			InputCacheRead:  formatOpenRouterUSDPerToken(cacheReadUSD * baseMult),
		})
	}
	return out
}

func openRouterProviderPeakPricingOverride(
	group *Group,
	promptUSD, completionUSD, cacheReadUSD, baseMult float64,
) (OpenRouterProviderPricingOverride, bool) {
	if group == nil || !group.PeakRateEnabled || group.PeakRateMultiplier <= 1 {
		return OpenRouterProviderPricingOverride{}, false
	}
	start, okStart := openRouterProviderLocalHMToUTCMinutes(group.PeakStart, timezone.Location(), time.Now())
	end, okEnd := openRouterProviderLocalHMToUTCMinutes(group.PeakEnd, timezone.Location(), time.Now())
	if !okStart || !okEnd || start == end {
		return OpenRouterProviderPricingOverride{}, false
	}
	peakMult := group.PeakRateMultiplier
	return OpenRouterProviderPricingOverride{
		UTCStart:       start,
		UTCEnd:         end,
		Prompt:         formatOpenRouterUSDPerToken(promptUSD * baseMult * peakMult),
		Completion:     formatOpenRouterUSDPerToken(completionUSD * baseMult * peakMult),
		InputCacheRead: formatOpenRouterUSDPerToken(cacheReadUSD * baseMult * peakMult),
	}, true
}

func openRouterProviderLocalHMToUTCMinutes(hm string, loc *time.Location, ref time.Time) (int, bool) {
	if loc == nil {
		loc = time.UTC
	}
	total, ok := parseMinutes(hm)
	if !ok {
		return 0, false
	}
	hour := total / 60
	minute := total % 60
	local := time.Date(ref.Year(), ref.Month(), ref.Day(), hour, minute, 0, 0, loc)
	utc := local.UTC()
	return utc.Hour()*100 + utc.Minute(), true
}

func openRouterProviderCapacityTPM(cfg OpenRouterProviderConfig, sourceID string) *int64 {
	if cfg.ModelCapacityTPM != nil {
		if v, ok := cfg.ModelCapacityTPM[sourceID]; ok && v > 0 {
			return &v
		}
	}
	return cfg.CapacityTPM
}

func openRouterProviderDatacenters(cfg OpenRouterProviderConfig) []OpenRouterProviderDatacenter {
	codes := cfg.DatacenterCountryCodes
	if len(codes) == 0 {
		codes = []string{"US"}
	}
	out := make([]OpenRouterProviderDatacenter, 0, len(codes))
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		out = append(out, OpenRouterProviderDatacenter{CountryCode: strings.ToUpper(code)})
	}
	if len(out) == 0 {
		return []OpenRouterProviderDatacenter{{CountryCode: "US"}}
	}
	return out
}

func openRouterAppendUniqueString(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}
