package service

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const (
	openRouterProviderDefaultQuantization = "fp16"
)

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

func openRouterProviderInputModalityTypes(meta *PublicCatalogModel) []string {
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

func openRouterProviderOutputModalityType(meta *PublicCatalogModel) string {
	if meta == nil {
		return "text"
	}
	switch meta.Pricing.BillingMode {
	case "image":
		return "image"
	case "video":
		return "video"
	default:
		return "text"
	}
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

func openRouterProviderBuildModelDocument(
	cfg OpenRouterProviderConfig,
	group *Group,
	meta *PublicCatalogModel,
	publicID, sourceID, slug string,
	created int64,
	promptUSD, completionUSD, cacheReadUSD, baseMult float64,
) OpenRouterProviderModel {
	outType := openRouterProviderOutputModalityType(meta)
	item := OpenRouterProviderModel{
		SchemaVersion:    openRouterProviderSchemaVersion,
		ID:               publicID,
		Name:             openRouterProviderDisplayName(cfg, publicID, meta),
		Description:      openRouterProviderDescription(cfg, publicID, meta),
		Created:          created,
		Quantization:     openRouterProviderDefaultQuantization,
		InputModalities:  openRouterProviderBuildInputModalities(cfg, group, meta, sourceID, promptUSD, cacheReadUSD, baseMult, outType),
		OutputModalities: openRouterProviderBuildOutputModalities(cfg, group, meta, sourceID, completionUSD, baseMult, outType),
		IsReady:          true,
		OpenRouter: map[string]string{
			"slug": slug,
		},
		Datacenters: openRouterProviderDatacenters(cfg),
	}
	return item
}

func openRouterProviderBuildInputModalities(
	cfg OpenRouterProviderConfig,
	group *Group,
	meta *PublicCatalogModel,
	sourceID string,
	promptUSD, cacheReadUSD, baseMult float64,
	outType string,
) []OpenRouterProviderInputModality {
	types := openRouterProviderInputModalityTypes(meta)
	// Image/video generation still needs a text prompt input modality.
	if outType == "image" || outType == "video" {
		types = []string{"text"}
	}
	out := make([]OpenRouterProviderInputModality, 0, len(types))
	for _, typ := range types {
		switch typ {
		case "text":
			mod := OpenRouterProviderInputModality{
				Type: "text",
				SupportedInputs: map[string]any{
					"max_context_length": OpenRouterProviderQuantity{
						Value: openRouterProviderContextLength(cfg, meta),
						Unit:  "token",
					},
				},
			}
			if outType == "text" {
				mod.Pricing = openRouterProviderBuildTextInputPricing(group, meta, promptUSD, cacheReadUSD, baseMult)
				if tpm := openRouterProviderCapacityTPM(cfg, sourceID); tpm != nil && *tpm > 0 {
					mod.Capacity = []OpenRouterProviderCapacityEntry{{
						Type:  "prompt",
						Unit:  "token",
						Per:   "minute",
						Value: *tpm,
					}}
				}
			}
			out = append(out, mod)
		case "image":
			out = append(out, OpenRouterProviderInputModality{
				Type: "image",
				SupportedInputs: map[string]any{
					"sources": map[string]any{"type": "enum", "values": []any{"url", "base64"}},
					"formats": map[string]any{
						"type":   "enum",
						"values": []any{"image/png", "image/jpeg", "image/webp", "image/gif"},
					},
				},
			})
		case "file":
			out = append(out, OpenRouterProviderInputModality{
				Type: "file",
				SupportedInputs: map[string]any{
					"formats": map[string]any{"type": "enum", "values": []any{"application/pdf"}},
				},
			})
		}
	}
	if len(out) == 0 {
		return []OpenRouterProviderInputModality{{Type: "text"}}
	}
	return out
}

func openRouterProviderBuildOutputModalities(
	cfg OpenRouterProviderConfig,
	group *Group,
	meta *PublicCatalogModel,
	sourceID string,
	completionUSD, baseMult float64,
	outType string,
) []OpenRouterProviderOutputModality {
	switch outType {
	case "image":
		streaming := false
		mod := OpenRouterProviderOutputModality{
			Type:      "image",
			Streaming: &streaming,
		}
		if unit := openRouterProviderMediaUnitUSD(meta); unit > 0 {
			mod.Pricing = []OpenRouterProviderPriceEntry{{
				Type:    "completion",
				Unit:    "image",
				CostUSD: formatOpenRouterUSDPerToken(unit * baseMult),
			}}
		}
		return []OpenRouterProviderOutputModality{mod}
	case "video":
		streaming := false
		mod := OpenRouterProviderOutputModality{
			Type:      "video",
			Streaming: &streaming,
		}
		if unit := openRouterProviderMediaUnitUSD(meta); unit > 0 {
			mod.Pricing = []OpenRouterProviderPriceEntry{{
				Type:    "completion",
				Unit:    "second",
				CostUSD: formatOpenRouterUSDPerToken(unit * baseMult),
			}}
		}
		return []OpenRouterProviderOutputModality{mod}
	default:
		streaming := true
		maxOut := openRouterProviderMaxOutputLength(cfg, meta)
		mod := OpenRouterProviderOutputModality{
			Type: "text",
			MaxLength: &OpenRouterProviderQuantity{
				Value: maxOut,
				Unit:  "token",
			},
			Streaming:           &streaming,
			SupportedParameters: openRouterProviderTextSupportedParameters(meta, maxOut),
			Pricing:             openRouterProviderBuildTextOutputPricing(group, meta, completionUSD, baseMult),
		}
		if tpm := openRouterProviderCapacityTPM(cfg, sourceID); tpm != nil && *tpm > 0 {
			mod.Capacity = []OpenRouterProviderCapacityEntry{{
				Type:  "completion",
				Unit:  "token",
				Per:   "minute",
				Value: *tpm,
			}}
		}
		return []OpenRouterProviderOutputModality{mod}
	}
}

func openRouterProviderTextSupportedParameters(meta *PublicCatalogModel, maxOut int) map[string]OpenRouterProviderParamDescriptor {
	min0 := 0.0
	max1 := 1.0
	maxTokensMin := 1.0
	maxTokensMax := float64(maxOut)
	if maxTokensMax < 1 {
		maxTokensMax = 1
	}
	maxStop := 4
	params := map[string]OpenRouterProviderParamDescriptor{
		"temperature": {Type: "range", Min: &min0, Max: &max1},
		"top_p":       {Type: "range", Min: &min0, Max: &max1},
		"max_tokens":  {Type: "integer", Min: &maxTokensMin, Max: &maxTokensMax, Unit: "token"},
		"stop":        {Type: "array", MaxItems: &maxStop},
	}
	for _, feat := range openRouterProviderSupportedFeatureFlags(meta) {
		params[feat] = OpenRouterProviderParamDescriptor{Type: "boolean"}
	}
	return params
}

func openRouterProviderSupportedFeatureFlags(meta *PublicCatalogModel) []string {
	if meta == nil || len(meta.Capabilities) == 0 {
		return nil
	}
	features := make([]string, 0, 6)
	for _, cap := range meta.Capabilities {
		switch cap {
		case "tool_use":
			features = openRouterAppendUniqueString(features, "tools")
		case "response_schema":
			features = openRouterAppendUniqueString(features, "structured_outputs")
		case "reasoning":
			features = openRouterAppendUniqueString(features, "reasoning")
		case "web_search":
			features = openRouterAppendUniqueString(features, "web_search")
		}
	}
	return features
}

func openRouterProviderBuildTextInputPricing(
	group *Group,
	meta *PublicCatalogModel,
	promptUSD, cacheReadUSD, baseMult float64,
) []OpenRouterProviderPriceEntry {
	out := make([]OpenRouterProviderPriceEntry, 0, 4)
	usePeak := !openRouterProviderHasTokenTiers(meta)
	if promptUSD > 0 {
		entry := OpenRouterProviderPriceEntry{
			Type:    "prompt",
			Unit:    "token",
			CostUSD: formatOpenRouterUSDPerToken(promptUSD * baseMult),
		}
		entry.Overrides = openRouterProviderTierPriceOverrides(meta, "input", baseMult)
		out = append(out, entry)
		if usePeak {
			out = append(out, openRouterProviderPeakPriceEntries(group, "prompt", "token", promptUSD, baseMult)...)
		}
	}
	if cacheReadUSD > 0 {
		entry := OpenRouterProviderPriceEntry{
			Type:    "cached_prompt",
			Unit:    "token",
			CostUSD: formatOpenRouterUSDPerToken(cacheReadUSD * baseMult),
		}
		entry.Overrides = openRouterProviderTierPriceOverrides(meta, "cache_read", baseMult)
		out = append(out, entry)
		if usePeak {
			out = append(out, openRouterProviderPeakPriceEntries(group, "cached_prompt", "token", cacheReadUSD, baseMult)...)
		}
	}
	return out
}

func openRouterProviderBuildTextOutputPricing(
	group *Group,
	meta *PublicCatalogModel,
	completionUSD, baseMult float64,
) []OpenRouterProviderPriceEntry {
	if completionUSD <= 0 {
		return nil
	}
	entry := OpenRouterProviderPriceEntry{
		Type:    "completion",
		Unit:    "token",
		CostUSD: formatOpenRouterUSDPerToken(completionUSD * baseMult),
	}
	entry.Overrides = openRouterProviderTierPriceOverrides(meta, "output", baseMult)
	out := []OpenRouterProviderPriceEntry{entry}
	if !openRouterProviderHasTokenTiers(meta) {
		out = append(out, openRouterProviderPeakPriceEntries(group, "completion", "token", completionUSD, baseMult)...)
	}
	return out
}

func openRouterProviderHasTokenTiers(meta *PublicCatalogModel) bool {
	if meta == nil || len(meta.Pricing.Tiers) <= 1 {
		return false
	}
	for i, tier := range meta.Pricing.Tiers {
		if i == 0 {
			continue
		}
		if tier.MinTokens > 0 {
			return true
		}
	}
	return false
}

// openRouterProviderTierPriceOverrides maps TokenKey long-context tiers onto schema 2.4
// when.prompt_tokens predicates. Tier pricing wins over peak-window pricing (same as flat).
func openRouterProviderTierPriceOverrides(meta *PublicCatalogModel, kind string, baseMult float64) []OpenRouterProviderPriceOverride {
	if meta == nil || len(meta.Pricing.Tiers) <= 1 {
		return nil
	}
	out := make([]OpenRouterProviderPriceOverride, 0, 2)
	for i, tier := range meta.Pricing.Tiers {
		if i == 0 || tier.MinTokens <= 0 {
			continue
		}
		if len(out) >= 2 {
			break
		}
		var cost float64
		switch kind {
		case "input":
			cost = tier.InputPer1KTokens / 1000 * baseMult
		case "output":
			cost = tier.OutputPer1KTokens / 1000 * baseMult
		case "cache_read":
			if tier.CacheReadPer1K <= 0 {
				continue
			}
			cost = tier.CacheReadPer1K / 1000 * baseMult
		default:
			continue
		}
		if cost <= 0 {
			continue
		}
		out = append(out, OpenRouterProviderPriceOverride{
			When: map[string]any{
				"prompt_tokens": map[string]any{"gte": tier.MinTokens},
			},
			CostUSD: formatOpenRouterUSDPerToken(cost),
		})
	}
	return out
}

func openRouterProviderPeakPriceEntries(
	group *Group,
	priceType, unit string,
	baseUSD, baseMult float64,
) []OpenRouterProviderPriceEntry {
	if group == nil || !group.PeakRateEnabled || group.PeakRateMultiplier <= 1 || baseUSD <= 0 {
		return nil
	}
	start, okStart := openRouterProviderLocalHMToUTCMinutes(group.PeakStart, timezone.Location(), time.Now())
	end, okEnd := openRouterProviderLocalHMToUTCMinutes(group.PeakEnd, timezone.Location(), time.Now())
	if !okStart || !okEnd || start == end {
		return nil
	}
	// Prefer tier overrides over peak windows when both exist on the same model.
	// Peak entries are only emitted when the caller did not already attach tier overrides
	// to the base entry; callers still may emit peak for SKUs without tiers.
	s, e := start, end
	return []OpenRouterProviderPriceEntry{{
		Type:     priceType,
		Unit:     unit,
		CostUSD:  formatOpenRouterUSDPerToken(baseUSD * baseMult * group.PeakRateMultiplier),
		UTCStart: &s,
		UTCEnd:   &e,
	}}
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
