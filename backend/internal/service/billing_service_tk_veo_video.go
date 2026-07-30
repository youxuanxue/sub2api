package service

import (
	"math"
	"strings"
)

// Vertex Veo video billing — official USD/s from cloud.google.com/vertex-ai/generative-ai/pricing
// (captured 2026-06-13 / aligned 2026-07-30). Billed per generated output second (success_only).

type veoVideoTier struct {
	Resolution string
	WithAudio  float64 // USD/s with audio (or sole rate when audio N/A)
	Silent     float64 // USD/s video-only; 0 → use WithAudio
}

type veoVideoModelSpec struct {
	Tiers             []veoVideoTier
	DefaultResolution string
	SupportsAudio     bool
}

func tkIsVeoVideoModel(model string) bool {
	_, ok := veoVideoModelSpecs()[strings.ToLower(strings.TrimSpace(model))]
	return ok
}

func veoVideoModelSpecs() map[string]veoVideoModelSpec {
	return map[string]veoVideoModelSpec{
		"veo-2.0-generate-001": {
			Tiers:             []veoVideoTier{{VideoBillingResolution720P, 0.50, 0.50}},
			DefaultResolution: VideoBillingResolution720P,
			SupportsAudio:     false,
		},
		"veo-3.0-generate-001": {
			Tiers: []veoVideoTier{
				{VideoBillingResolution720P, 0.40, 0.20},
				{VideoBillingResolution1080P, 0.40, 0.20},
			},
			DefaultResolution: VideoBillingResolution1080P,
			SupportsAudio:     true,
		},
		"veo-3.0-fast-generate-001": {
			Tiers: []veoVideoTier{
				{VideoBillingResolution720P, 0.10, 0.10},
				{VideoBillingResolution1080P, 0.12, 0.12},
			},
			DefaultResolution: VideoBillingResolution1080P,
			SupportsAudio:     false,
		},
		"veo-3.1-generate-001": {
			Tiers: []veoVideoTier{
				{VideoBillingResolution720P, 0.40, 0.20},
				{VideoBillingResolution1080P, 0.40, 0.20},
				{VideoBillingResolution4K, 0.60, 0.40},
			},
			DefaultResolution: VideoBillingResolution1080P,
			SupportsAudio:     true,
		},
		"veo-3.1-generate-preview": {
			Tiers: []veoVideoTier{
				{VideoBillingResolution720P, 0.40, 0.20},
				{VideoBillingResolution1080P, 0.40, 0.20},
				{VideoBillingResolution4K, 0.60, 0.40},
			},
			DefaultResolution: VideoBillingResolution1080P,
			SupportsAudio:     true,
		},
		"veo-3.1-fast-generate-001": {
			Tiers: []veoVideoTier{
				{VideoBillingResolution720P, 0.10, 0.10},
				{VideoBillingResolution1080P, 0.12, 0.12},
				{VideoBillingResolution4K, 0.30, 0.25},
			},
			DefaultResolution: VideoBillingResolution1080P,
			SupportsAudio:     true,
		},
		"veo-3.1-fast-generate-preview": {
			Tiers: []veoVideoTier{
				{VideoBillingResolution720P, 0.10, 0.10},
				{VideoBillingResolution1080P, 0.12, 0.12},
				{VideoBillingResolution4K, 0.30, 0.25},
			},
			DefaultResolution: VideoBillingResolution1080P,
			SupportsAudio:     true,
		},
		"veo-3.1-lite-generate-preview": {
			Tiers: []veoVideoTier{
				{VideoBillingResolution720P, 0.05, 0.03},
				{VideoBillingResolution1080P, 0.08, 0.05},
			},
			DefaultResolution: VideoBillingResolution1080P,
			SupportsAudio:     true,
		},
	}
}

func tkVeoDefaultResolution(model string) (string, bool) {
	spec, ok := veoVideoModelSpecs()[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return "", false
	}
	if spec.DefaultResolution != "" {
		return spec.DefaultResolution, true
	}
	return VideoBillingResolution1080P, true
}

func tkVeoSupportsResolution(model, resolution string) bool {
	spec, ok := veoVideoModelSpecs()[strings.ToLower(strings.TrimSpace(model))]
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

func tkVeoVideoUnitPriceUSD(model, resolution string, opts *VideoBillingOptions) (float64, bool) {
	spec, ok := veoVideoModelSpecs()[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return 0, false
	}
	resolution = tkVideoNormalizeResolution(model, resolution)
	var tier *veoVideoTier
	for i := range spec.Tiers {
		if spec.Tiers[i].Resolution == resolution {
			tier = &spec.Tiers[i]
			break
		}
	}
	if tier == nil {
		for i := range spec.Tiers {
			if spec.Tiers[i].Resolution == spec.DefaultResolution {
				tier = &spec.Tiers[i]
				break
			}
		}
	}
	if tier == nil {
		return 0, false
	}
	withAudio := videoBillingWithAudio(opts, true)
	if !spec.SupportsAudio || withAudio {
		return tier.WithAudio, true
	}
	if tier.Silent > 0 {
		return tier.Silent, true
	}
	return tier.WithAudio, true
}

func tkVeoVideoHoldUnitPriceUSD(model string) float64 {
	spec, ok := veoVideoModelSpecs()[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return 0
	}
	max := 0.0
	audioModes := []bool{true}
	if spec.SupportsAudio {
		audioModes = []bool{true, false}
	}
	for _, withAudio := range audioModes {
		for _, tier := range spec.Tiers {
			p := tier.WithAudio
			if spec.SupportsAudio && !withAudio && tier.Silent > 0 {
				p = tier.Silent
			}
			max = math.Max(max, p)
		}
	}
	return max
}

func tkVeoVideoMinUnitPriceUSD(model string) (float64, bool) {
	spec, ok := veoVideoModelSpecs()[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return 0, false
	}
	min := math.MaxFloat64
	audioModes := []bool{true}
	if spec.SupportsAudio {
		audioModes = []bool{true, false}
	}
	for _, withAudio := range audioModes {
		for _, tier := range spec.Tiers {
			p := tier.WithAudio
			if spec.SupportsAudio && !withAudio && tier.Silent > 0 {
				p = tier.Silent
			}
			if p < min {
				min = p
			}
		}
	}
	if min == math.MaxFloat64 {
		return 0, false
	}
	return min, true
}

// VeoVideoCatalogTier is one resolution row for the public catalog.
type VeoVideoCatalogTier struct {
	Resolution      string
	PerSecond       float64
	PerSecondSilent float64
	DefaultForModel bool
}

func tkVeoVideoCatalogTiers(model string) []VeoVideoCatalogTier {
	spec, ok := veoVideoModelSpecs()[strings.ToLower(strings.TrimSpace(model))]
	if !ok {
		return nil
	}
	out := make([]VeoVideoCatalogTier, 0, len(spec.Tiers))
	for _, tier := range spec.Tiers {
		row := VeoVideoCatalogTier{
			Resolution:      tier.Resolution,
			PerSecond:       tier.WithAudio,
			DefaultForModel: tier.Resolution == spec.DefaultResolution,
		}
		if spec.SupportsAudio && tier.Silent > 0 && tier.Silent != tier.WithAudio {
			row.PerSecondSilent = tier.Silent
		}
		out = append(out, row)
	}
	return out
}
