package service

import (
	"math"
	"strings"
)

// VolcEngine Seedance video billing follows the official token formula:
//   tokens = duration × W × H × fps / 1024
//   CNY cost = tokens × (CNY per million tokens) / 1e6
// Rates captured from https://www.volcengine.com/docs/82379/1099320 (2026-06-12/2026-07-30).
// USD conversion uses CNY/USD=6.7; volcengine official_list_base_tax (×1.06) is applied
// at presentation/billing time via tkApplyVolcengineVideoListTax.

const (
	seedanceVideoFPS            = 24
	seedanceCNYPerUSD           = 6.7
	seedanceTokensPerSecond480  = 50220.0 / 5 // official 480p 16:9 5s example (doubao-seedance-2.0)
	seedanceTokensPerSecond720  = 21600.0     // 1280×720×24/1024
	seedanceTokensPerSecond1080 = 48600.0     // 1920×1080×24/1024
	seedanceTokensPerSecond4K   = 194400.0    // 3840×2160×24/1024
)

type seedanceVideoTierRate struct {
	Resolution      string
	TokensPerSecond float64
	CNYPerMillion   float64
}

type seedanceVideoModelSpec struct {
	Tiers             []seedanceVideoTierRate
	DefaultResolution string
	// SupportsAudioPricing: when true, WithAudio selects CNYWithAudio vs CNYSilent on tiers that set both.
	SupportsAudioPricing bool
	CNYWithAudio         float64 // 0 → use tier CNYPerMillion
	CNYSilent            float64 // 0 → use tier CNYPerMillion
}

func tkIsSeedanceVideoModel(model string) bool {
	_, ok := seedanceVideoModelSpecs()[normalizeSeedanceModelID(model)]
	return ok
}

func normalizeSeedanceModelID(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(m, "seedance-") && !strings.HasPrefix(m, "doubao-") {
		return "doubao-" + m
	}
	return m
}

func seedanceVideoModelSpecs() map[string]seedanceVideoModelSpec {
	// Shared resolution ladder token counts (16:9, 24fps) from the official table.
	t480 := seedanceVideoTierRate{VideoBillingResolution480P, seedanceTokensPerSecond480, 46}
	t720 := seedanceVideoTierRate{VideoBillingResolution720P, seedanceTokensPerSecond720, 46}
	t1080 := seedanceVideoTierRate{VideoBillingResolution1080P, seedanceTokensPerSecond1080, 51}
	t4k := seedanceVideoTierRate{VideoBillingResolution4K, seedanceTokensPerSecond4K, 26}

	rate15 := func(cny float64) float64 { return cny }
	t480_15 := seedanceVideoTierRate{VideoBillingResolution480P, seedanceTokensPerSecond480, rate15(46)}
	t720_15 := seedanceVideoTierRate{VideoBillingResolution720P, seedanceTokensPerSecond720, rate15(46)}
	t1080_15 := seedanceVideoTierRate{VideoBillingResolution1080P, seedanceTokensPerSecond1080, rate15(51)}

	return map[string]seedanceVideoModelSpec{
		"doubao-seedance-1-0-pro-250528": {
			Tiers: []seedanceVideoTierRate{
				{VideoBillingResolution480P, seedanceTokensPerSecond480, 15},
				{VideoBillingResolution720P, seedanceTokensPerSecond720, 15},
				{VideoBillingResolution1080P, seedanceTokensPerSecond1080, 15},
			},
			DefaultResolution: VideoBillingResolution1080P,
		},
		"seedance-1-0-pro-250528": {
			Tiers: []seedanceVideoTierRate{
				{VideoBillingResolution480P, seedanceTokensPerSecond480, 15},
				{VideoBillingResolution720P, seedanceTokensPerSecond720, 15},
				{VideoBillingResolution1080P, seedanceTokensPerSecond1080, 15},
			},
			DefaultResolution: VideoBillingResolution1080P,
		},
		"doubao-seedance-1-0-pro-fast-251015": {
			Tiers: []seedanceVideoTierRate{
				{VideoBillingResolution480P, seedanceTokensPerSecond480, 4.2},
				{VideoBillingResolution720P, seedanceTokensPerSecond720, 4.2},
			},
			DefaultResolution: VideoBillingResolution720P,
		},
		"doubao-seedance-1-5-pro-251215": {
			Tiers:                []seedanceVideoTierRate{t480_15, t720_15, t1080_15},
			DefaultResolution:    VideoBillingResolution1080P,
			SupportsAudioPricing: true,
			CNYWithAudio:         16,
			CNYSilent:            8,
		},
		"doubao-seedance-2-0-260128": {
			Tiers:             []seedanceVideoTierRate{t480, t720, t1080, t4k},
			DefaultResolution: VideoBillingResolution1080P,
		},
		"doubao-seedance-2-0-fast-260128": {
			Tiers: []seedanceVideoTierRate{
				{VideoBillingResolution480P, seedanceTokensPerSecond480, 37},
				{VideoBillingResolution720P, seedanceTokensPerSecond720, 37},
			},
			DefaultResolution: VideoBillingResolution720P,
		},
	}
}

func tkSeedanceDefaultResolution(model string) string {
	spec, ok := seedanceVideoModelSpecs()[normalizeSeedanceModelID(model)]
	if !ok || spec.DefaultResolution == "" {
		return VideoBillingResolution1080P
	}
	return spec.DefaultResolution
}

func tkSeedanceSupportsResolution(model, resolution string) bool {
	spec, ok := seedanceVideoModelSpecs()[normalizeSeedanceModelID(model)]
	if !ok {
		return false
	}
	resolution = NormalizeVideoBillingResolutionOrDefault(resolution)
	for _, tier := range spec.Tiers {
		if tier.Resolution == resolution {
			return true
		}
	}
	return false
}

// tkSeedanceVideoUnitPriceUSDPreTax returns the official-derived USD/s before volcengine list tax.
func tkSeedanceVideoUnitPriceUSDPreTax(model, resolution string, withAudio bool) (float64, bool) {
	spec, ok := seedanceVideoModelSpecs()[normalizeSeedanceModelID(model)]
	if !ok {
		return 0, false
	}
	resolution = NormalizeVideoBillingResolutionOrDefault(resolution)
	if !tkSeedanceSupportsResolution(model, resolution) {
		resolution = spec.DefaultResolution
	}
	var tier *seedanceVideoTierRate
	for i := range spec.Tiers {
		if spec.Tiers[i].Resolution == resolution {
			tier = &spec.Tiers[i]
			break
		}
	}
	if tier == nil {
		return 0, false
	}
	cnyPerM := tier.CNYPerMillion
	if spec.SupportsAudioPricing {
		if withAudio && spec.CNYWithAudio > 0 {
			cnyPerM = spec.CNYWithAudio
		} else if !withAudio && spec.CNYSilent > 0 {
			cnyPerM = spec.CNYSilent
		}
	}
	cnyPerSecond := tier.TokensPerSecond * cnyPerM / 1e6
	return cnyPerSecond / seedanceCNYPerUSD, true
}

func tkApplyVolcengineVideoListTax(usdPreTax float64) float64 {
	multiplier, ok := tkBaseTaxMultiplierForProvider("volcengine")
	if !ok {
		return usdPreTax
	}
	return tkApplyBaseTaxMultiplier(usdPreTax, multiplier)
}

// tkSeedanceVideoUnitPriceUSD is the billed/presented USD/s (pre-tax × volcengine list tax).
func tkSeedanceVideoUnitPriceUSD(model, resolution string, withAudio bool) (float64, bool) {
	pre, ok := tkSeedanceVideoUnitPriceUSDPreTax(model, resolution, withAudio)
	if !ok {
		return 0, false
	}
	return tkApplyVolcengineVideoListTax(pre), true
}

// tkSeedanceVideoHoldUnitPriceUSD returns the max USD/s across supported tiers (and with-audio when applicable).
func tkSeedanceVideoHoldUnitPriceUSD(model string) float64 {
	spec, ok := seedanceVideoModelSpecs()[normalizeSeedanceModelID(model)]
	if !ok {
		return 0
	}
	max := 0.0
	audioModes := []bool{false}
	if spec.SupportsAudioPricing {
		audioModes = []bool{true, false}
	}
	for _, withAudio := range audioModes {
		for _, tier := range spec.Tiers {
			if p, ok := tkSeedanceVideoUnitPriceUSD(model, tier.Resolution, withAudio); ok {
				max = math.Max(max, p)
			}
		}
	}
	return max
}

// SeedanceVideoCatalogTier is one resolution (+ optional silent) price row for public catalog.
type SeedanceVideoCatalogTier struct {
	Resolution      string
	PerSecond       float64
	PerSecondSilent float64 // >0 only when audio pricing applies
	DefaultForModel bool
}

func tkSeedanceVideoCatalogTiers(model string) []SeedanceVideoCatalogTier {
	spec, ok := seedanceVideoModelSpecs()[normalizeSeedanceModelID(model)]
	if !ok {
		return nil
	}
	out := make([]SeedanceVideoCatalogTier, 0, len(spec.Tiers))
	for _, tier := range spec.Tiers {
		row := SeedanceVideoCatalogTier{
			Resolution:      tier.Resolution,
			DefaultForModel: tier.Resolution == spec.DefaultResolution,
		}
		if p, ok := tkSeedanceVideoUnitPriceUSD(model, tier.Resolution, true); ok {
			row.PerSecond = p
		}
		if spec.SupportsAudioPricing {
			if p, ok := tkSeedanceVideoUnitPriceUSD(model, tier.Resolution, false); ok {
				row.PerSecondSilent = p
			}
		}
		out = append(out, row)
	}
	return out
}
