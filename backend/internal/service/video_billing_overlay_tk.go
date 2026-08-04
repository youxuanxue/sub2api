package service

import (
	"fmt"
	"math"
	"strings"
)

// PricingVideoTier is one resolution (and optional audio / image-input) bracket
// owned by tk_pricing_overlay.json "video_price_tiers". Pre-tax official list
// USD/s; tkPresentLiteLLMModelPricingFromSnapshot applies provider base tax at read time.
type PricingVideoTier struct {
	Resolution                   string
	PerSecond                    float64
	PerSecondSilent              float64
	InputImageSurchargePerSecond float64
	DefaultForModel              bool
}

func tkOverlayVideoModelKey(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(m, "seedance-") && !strings.HasPrefix(m, "doubao-") {
		return "doubao-" + m
	}
	return m
}

func tkOverlayRawVideoEntry(model string) *LiteLLMModelPricing {
	return tkOverlayRawVideoEntryFromSnapshot(loadTKPricingOverlaySnapshot(), model)
}

func tkOverlayRawVideoEntryFromSnapshot(snapshot *tkPricingOverlaySnapshot, model string) *LiteLLMModelPricing {
	if snapshot == nil {
		return nil
	}
	for _, key := range []string{tkOverlayVideoModelKey(model), strings.ToLower(strings.TrimSpace(model))} {
		if p, ok := snapshot.Models[key]; ok && p != nil && len(p.VideoPriceTiers) > 0 {
			return p
		}
	}
	return nil
}

// tkOverlayVideoPricing returns overlay video tiers with base tax applied (billing/catalog SSOT).
func tkOverlayVideoPricing(model string) *LiteLLMModelPricing {
	return tkOverlayVideoPricingFromSnapshot(loadTKPricingOverlaySnapshot(), model)
}

func tkOverlayVideoPricingFromSnapshot(snapshot *tkPricingOverlaySnapshot, model string) *LiteLLMModelPricing {
	raw := tkOverlayRawVideoEntryFromSnapshot(snapshot, model)
	if raw == nil {
		return nil
	}
	return tkPresentLiteLLMModelPricingFromSnapshot(raw, snapshot)
}

func tkIsGrokImagineVideoModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "grok-imagine-video") && tkOverlayRawVideoEntry(model) != nil
}

func tkOverlayVideoDefaultResolutionFromPricing(p *LiteLLMModelPricing) string {
	if p == nil {
		return VideoBillingResolution480P
	}
	if p.DefaultVideoResolution != "" {
		return NormalizeVideoBillingResolutionOrDefault(p.DefaultVideoResolution)
	}
	for _, tier := range p.VideoPriceTiers {
		if tier.DefaultForModel {
			return tier.Resolution
		}
	}
	if len(p.VideoPriceTiers) > 0 {
		return p.VideoPriceTiers[0].Resolution
	}
	return VideoBillingResolution480P
}

func tkOverlayVideoSupportsResolutionFromPricing(p *LiteLLMModelPricing, resolution string) bool {
	if p == nil {
		return false
	}
	resolution = NormalizeVideoBillingResolutionOrDefault(resolution)
	for _, tier := range p.VideoPriceTiers {
		if tier.Resolution == resolution {
			return true
		}
	}
	return false
}

func tkOverlayVideoTierForResolution(p *LiteLLMModelPricing, resolution string) *PricingVideoTier {
	if p == nil {
		return nil
	}
	resolution = NormalizeVideoBillingResolutionOrDefault(resolution)
	for i := range p.VideoPriceTiers {
		if p.VideoPriceTiers[i].Resolution == resolution {
			return &p.VideoPriceTiers[i]
		}
	}
	for i := range p.VideoPriceTiers {
		if p.VideoPriceTiers[i].DefaultForModel {
			return &p.VideoPriceTiers[i]
		}
	}
	if len(p.VideoPriceTiers) > 0 {
		return &p.VideoPriceTiers[0]
	}
	return nil
}

func tkOverlayVideoUnitPriceUSD(model, resolution string, opts *VideoBillingOptions) (float64, bool) {
	return tkOverlayVideoUnitPriceUSDFromSnapshot(loadTKPricingOverlaySnapshot(), model, resolution, opts)
}

func tkOverlayVideoUnitPriceUSDFromSnapshot(snapshot *tkPricingOverlaySnapshot, model, resolution string, opts *VideoBillingOptions) (float64, bool) {
	p := tkOverlayVideoPricingFromSnapshot(snapshot, model)
	if p == nil {
		return 0, false
	}
	if resolution = tkVideoNormalizeResolutionFromPricing(p, resolution); resolution == "" {
		resolution = tkOverlayVideoDefaultResolutionFromPricing(p)
	}
	tier := tkOverlayVideoTierForResolution(p, resolution)
	if tier == nil {
		return 0, false
	}
	withAudio := videoBillingWithAudio(opts, true)
	rate := tier.PerSecond
	if tier.PerSecondSilent > 0 && !withAudio {
		rate = tier.PerSecondSilent
	} else if !withAudio && tier.PerSecondSilent == 0 && tier.PerSecond > 0 {
		rate = tier.PerSecond
	}
	if videoBillingHasInputImage(opts) && tier.InputImageSurchargePerSecond > 0 {
		rate += tier.InputImageSurchargePerSecond
	}
	if rate <= 0 {
		return 0, false
	}
	return rate, true
}

func tkVideoNormalizeResolutionFromPricing(p *LiteLLMModelPricing, resolution string) string {
	if strings.TrimSpace(resolution) == "" {
		return tkOverlayVideoDefaultResolutionFromPricing(p)
	}
	resolution = NormalizeVideoBillingResolutionOrDefault(resolution)
	if p == nil {
		return resolution
	}
	if tkOverlayVideoSupportsResolutionFromPricing(p, resolution) {
		return resolution
	}
	return tkOverlayVideoDefaultResolutionFromPricing(p)
}

func tkOverlayVideoMinUnitPriceUSD(model string) (float64, bool) {
	return tkOverlayVideoMinUnitPriceUSDFromSnapshot(loadTKPricingOverlaySnapshot(), model)
}

func tkOverlayVideoMinUnitPriceUSDFromSnapshot(snapshot *tkPricingOverlaySnapshot, model string) (float64, bool) {
	p := tkOverlayVideoPricingFromSnapshot(snapshot, model)
	if p == nil {
		return 0, false
	}
	min := math.MaxFloat64
	for _, tier := range p.VideoPriceTiers {
		for _, rate := range []float64{tier.PerSecond, tier.PerSecondSilent} {
			if rate > 0 && rate < min {
				min = rate
			}
		}
	}
	if min == math.MaxFloat64 {
		return 0, false
	}
	return min, true
}

func tkOverlayVideoCatalogTiersFromSnapshot(snapshot *tkPricingOverlaySnapshot, model string) []PublicCatalogVideoTier {
	p := tkOverlayVideoPricingFromSnapshot(snapshot, model)
	if p == nil {
		return nil
	}
	out := make([]PublicCatalogVideoTier, 0, len(p.VideoPriceTiers))
	for _, tier := range p.VideoPriceTiers {
		row := PublicCatalogVideoTier{
			Resolution:      tier.Resolution,
			PerSecond:       tier.PerSecond,
			DefaultForModel: tier.DefaultForModel,
		}
		if tier.PerSecondSilent > 0 {
			s := tier.PerSecondSilent
			row.PerSecondSilent = &s
		}
		if tier.InputImageSurchargePerSecond > 0 {
			s := tier.InputImageSurchargePerSecond
			row.InputImageSurchargePerSecond = &s
		}
		out = append(out, row)
	}
	return out
}

func tkApplyBaseTaxToVideoTiers(tiers []PricingVideoTier, multiplier float64) []PricingVideoTier {
	if len(tiers) == 0 {
		return tiers
	}
	out := make([]PricingVideoTier, len(tiers))
	for i := range tiers {
		out[i] = tiers[i]
		out[i].PerSecond = tkApplyBaseTaxMultiplier(out[i].PerSecond, multiplier)
		out[i].PerSecondSilent = tkApplyBaseTaxMultiplier(out[i].PerSecondSilent, multiplier)
		out[i].InputImageSurchargePerSecond = tkApplyBaseTaxMultiplier(out[i].InputImageSurchargePerSecond, multiplier)
	}
	return out
}

func tkValidateAndBuildOverlayVideoTiers(raw []tkOverlayRawVideoTier, defaultResolution string, flatPerSecond float64) ([]PricingVideoTier, string, error) {
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("video_price_tiers must be a non-empty array")
	}
	out := make([]PricingVideoTier, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	defaultCount := 0
	declaredDefault := ""
	minRate := math.MaxFloat64
	for i, r := range raw {
		resolution, ok := canonicalOverlayVideoResolution(r.Resolution)
		if !ok {
			return nil, "", fmt.Errorf("video_price_tiers[%d].resolution %q is not canonical", i, r.Resolution)
		}
		if _, duplicate := seen[resolution]; duplicate {
			return nil, "", fmt.Errorf("video_price_tiers duplicates resolution %q", resolution)
		}
		seen[resolution] = struct{}{}
		if r.OutputCostPerSecond == nil || !finitePositive(*r.OutputCostPerSecond) {
			return nil, "", fmt.Errorf("video_price_tiers[%d].output_cost_per_second must be finite and > 0", i)
		}
		if r.OutputCostPerSecondSilent != nil && !finitePositive(*r.OutputCostPerSecondSilent) {
			return nil, "", fmt.Errorf("video_price_tiers[%d].output_cost_per_second_silent must be finite and > 0", i)
		}
		if r.InputImageSurchargePerSecond != nil && (!finiteNonNegative(*r.InputImageSurchargePerSecond)) {
			return nil, "", fmt.Errorf("video_price_tiers[%d].input_image_surcharge_per_second must be finite and >= 0", i)
		}
		tier := PricingVideoTier{
			Resolution:      resolution,
			PerSecond:       *r.OutputCostPerSecond,
			DefaultForModel: r.DefaultForModel,
		}
		if r.OutputCostPerSecondSilent != nil {
			tier.PerSecondSilent = *r.OutputCostPerSecondSilent
		}
		if r.InputImageSurchargePerSecond != nil {
			tier.InputImageSurchargePerSecond = *r.InputImageSurchargePerSecond
		}
		if tier.DefaultForModel {
			defaultCount++
			declaredDefault = resolution
		}
		minRate = math.Min(minRate, tier.PerSecond)
		if tier.PerSecondSilent > 0 {
			minRate = math.Min(minRate, tier.PerSecondSilent)
		}
		out = append(out, tier)
	}
	if defaultCount != 1 {
		return nil, "", fmt.Errorf("video_price_tiers must declare exactly one default_for_model, got %d", defaultCount)
	}
	if strings.TrimSpace(defaultResolution) != "" {
		canonicalDefault, ok := canonicalOverlayVideoResolution(defaultResolution)
		if !ok {
			return nil, "", fmt.Errorf("default_video_resolution %q is not canonical", defaultResolution)
		}
		if canonicalDefault != declaredDefault {
			return nil, "", fmt.Errorf("default_video_resolution %q does not match default_for_model %q", canonicalDefault, declaredDefault)
		}
	}
	if !finitePositive(flatPerSecond) || math.Abs(flatPerSecond-minRate) > 1e-12 {
		return nil, "", fmt.Errorf("output_cost_per_second must equal minimum video tier %.15g, got %.15g", minRate, flatPerSecond)
	}
	return out, declaredDefault, nil
}

func canonicalOverlayVideoResolution(value string) (string, bool) {
	switch value {
	case VideoBillingResolution480P, VideoBillingResolution720P, VideoBillingResolution1080P, VideoBillingResolution4K:
		return value, true
	default:
		return "", false
	}
}

func finitePositive(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
