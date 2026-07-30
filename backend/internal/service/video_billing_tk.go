package service

import (
	"math"
	"strings"
)

// VideoBillingOptions carries optional dimensions for tiered video billing.
// Nil GenerateAudio uses each model's upstream default (typically with audio).
type VideoBillingOptions struct {
	GenerateAudio *bool
	HasInputImage bool
}

func videoBillingWithAudio(opts *VideoBillingOptions, defaultWithAudio bool) bool {
	if opts != nil && opts.GenerateAudio != nil {
		return *opts.GenerateAudio
	}
	return defaultWithAudio
}

func videoBillingHasInputImage(opts *VideoBillingOptions) bool {
	return opts != nil && opts.HasInputImage
}

// tkVideoDefaultResolution returns the official default output resolution when the client omits one.
func tkVideoDefaultResolution(model string) string {
	if tkIsSeedanceVideoModel(model) {
		return tkSeedanceDefaultResolution(model)
	}
	if res, ok := tkVeoDefaultResolution(model); ok {
		return res
	}
	if tkIsGrokImagineVideoModel(model) {
		return VideoBillingResolution480P
	}
	return VideoBillingResolution480P
}

// tkVideoNormalizeResolution applies model-aware defaults and clamps unsupported tiers.
func tkVideoNormalizeResolution(model, resolution string) string {
	if strings.TrimSpace(resolution) == "" {
		return tkVideoDefaultResolution(model)
	}
	resolution = NormalizeVideoBillingResolutionOrDefault(resolution)
	if tkIsSeedanceVideoModel(model) {
		if tkSeedanceSupportsResolution(model, resolution) {
			return resolution
		}
		return tkSeedanceDefaultResolution(model)
	}
	if tkIsVeoVideoModel(model) {
		if tkVeoSupportsResolution(model, resolution) {
			return resolution
		}
		if def, ok := tkVeoDefaultResolution(model); ok {
			return def
		}
	}
	if tkIsGrokImagineVideoModel(model) {
		return resolution
	}
	return resolution
}

// tkVideoUnitPriceUSD returns the official-aligned USD/s for tiered video models.
func tkVideoUnitPriceUSD(model, resolution string, opts *VideoBillingOptions) (float64, bool) {
	if tkIsSeedanceVideoModel(model) {
		withAudio := videoBillingWithAudio(opts, true)
		return tkSeedanceVideoUnitPriceUSD(model, resolution, withAudio)
	}
	if price, ok := tkVeoVideoUnitPriceUSD(model, resolution, opts); ok {
		return price, true
	}
	if price, ok := tkGrokImagineVideoUnitPriceUSD(model, resolution, opts); ok {
		return price, true
	}
	return 0, false
}

// tkVideoHoldUnitPriceUSD is the conservative hold rate (max tier) for tiered models.
func tkVideoHoldUnitPriceUSD(model string) float64 {
	if tkIsSeedanceVideoModel(model) {
		return tkSeedanceVideoHoldUnitPriceUSD(model)
	}
	if p := tkVeoVideoHoldUnitPriceUSD(model); p > 0 {
		return p
	}
	if p := tkGrokImagineVideoHoldUnitPriceUSD(model); p > 0 {
		return p
	}
	return 0
}

// tkVideoMinUnitPriceUSD returns the cheapest official USD/s (for unpriced guard / catalog floor).
func tkVideoMinUnitPriceUSD(model string) (float64, bool) {
	if tkIsSeedanceVideoModel(model) {
		spec, ok := seedanceVideoModelSpecs()[normalizeSeedanceModelID(model)]
		if !ok {
			return 0, false
		}
		min := math.MaxFloat64
		audioModes := []bool{false}
		if spec.SupportsAudioPricing {
			audioModes = []bool{true, false}
		}
		for _, withAudio := range audioModes {
			for _, tier := range spec.Tiers {
				if p, ok := tkSeedanceVideoUnitPriceUSD(model, tier.Resolution, withAudio); ok && p < min {
					min = p
				}
			}
		}
		if min == math.MaxFloat64 {
			return 0, false
		}
		return min, true
	}
	if p, ok := tkVeoVideoMinUnitPriceUSD(model); ok {
		return p, true
	}
	if p, ok := tkGrokImagineVideoMinUnitPriceUSD(model); ok {
		return p, true
	}
	return 0, false
}

func tkIsTieredVideoModel(model string) bool {
	return tkIsSeedanceVideoModel(model) || tkIsVeoVideoModel(model) || tkIsGrokImagineVideoModel(model)
}
