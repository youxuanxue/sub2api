package service

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// tkDeepSeekPeakValleyWindow is one Beijing-time peak window [Start, End) in HH:MM.
type tkDeepSeekPeakValleyWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// tkDeepSeekPeakValleyPolicy is executable peak-valley pricing for DeepSeek direct
// API models. Stored in tk_pricing_overlay.json::_config; overlay entries keep
// off-peak (谷时) list prices and billing applies PeakMultiplier during windows.
type tkDeepSeekPeakValleyPolicy struct {
	Timezone       string                       `json:"timezone"`
	PeakMultiplier float64                      `json:"peak_multiplier"`
	Windows        []tkDeepSeekPeakValleyWindow `json:"windows"`
	ModelContains  []string                     `json:"model_contains"`
}

func (p tkDeepSeekPeakValleyPolicy) validate() error {
	if math.IsNaN(p.PeakMultiplier) || math.IsInf(p.PeakMultiplier, 0) || p.PeakMultiplier < 1 || p.PeakMultiplier > 4 {
		return fmt.Errorf("deepseek_peak_valley.peak_multiplier must be within [1,4], got %v", p.PeakMultiplier)
	}
	if len(p.Windows) == 0 {
		return fmt.Errorf("deepseek_peak_valley.windows must be non-empty")
	}
	if len(p.ModelContains) == 0 {
		return fmt.Errorf("deepseek_peak_valley.model_contains must be non-empty")
	}
	for i, w := range p.Windows {
		start, ok1 := parseMinutes(w.Start)
		end, ok2 := parseMinutes(w.End)
		if !ok1 {
			return fmt.Errorf("deepseek_peak_valley.windows[%d].start invalid: %q", i, w.Start)
		}
		if !ok2 {
			return fmt.Errorf("deepseek_peak_valley.windows[%d].end invalid: %q", i, w.End)
		}
		if start >= end {
			return fmt.Errorf("deepseek_peak_valley.windows[%d] requires end > start (no cross-day windows)", i)
		}
	}
	for i, substr := range p.ModelContains {
		value := strings.TrimSpace(substr)
		if value == "" || value != strings.ToLower(value) {
			return fmt.Errorf("deepseek_peak_valley.model_contains[%d] must be normalized lowercase", i)
		}
	}
	if tz := strings.TrimSpace(p.Timezone); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("deepseek_peak_valley.timezone invalid: %w", err)
		}
	}
	return nil
}

func loadTkDeepSeekPeakValleyPolicy() *tkDeepSeekPeakValleyPolicy {
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil || snapshot.DeepSeekPeakValley == nil {
		return nil
	}
	policy := *snapshot.DeepSeekPeakValley
	return &policy
}

func tkDeepSeekPeakValleyApplies(model string, pricingSource string) bool {
	if pricingSource == PricingSourceChannel {
		return false
	}
	policy := loadTkDeepSeekPeakValleyPolicy()
	if policy == nil {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, substr := range policy.ModelContains {
		if strings.Contains(lower, substr) {
			return true
		}
	}
	return false
}

// tkDeepSeekPeakMultiplierAt returns the DeepSeek upstream peak multiplier at `now`
// (1.0 off-peak, policy.PeakMultiplier during configured windows). Windows are
// evaluated in policy.Timezone (default Asia/Shanghai when empty).
func tkDeepSeekPeakMultiplierAt(now time.Time) float64 {
	policy := loadTkDeepSeekPeakValleyPolicy()
	if policy == nil || len(policy.Windows) == 0 || policy.PeakMultiplier <= 1 {
		return 1.0
	}
	loc := timezone.Location()
	if tz := strings.TrimSpace(policy.Timezone); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	t := now.In(loc)
	cur := t.Hour()*60 + t.Minute()
	for _, w := range policy.Windows {
		start, ok1 := parseMinutes(w.Start)
		end, ok2 := parseMinutes(w.End)
		if !ok1 || !ok2 || start >= end {
			continue
		}
		if cur >= start && cur < end {
			return policy.PeakMultiplier
		}
	}
	return 1.0
}

func tkApplyDeepSeekPeakValleyPricing(model string, pricing *ModelPricing, at time.Time, pricingSource string) *ModelPricing {
	if pricing == nil || !tkDeepSeekPeakValleyApplies(model, pricingSource) {
		return pricing
	}
	mult := tkDeepSeekPeakMultiplierAt(at)
	if mult <= 1 {
		return pricing
	}
	return tkScaleModelPricingByFactor(pricing, mult)
}

func tkScaleModelPricingByFactor(pricing *ModelPricing, factor float64) *ModelPricing {
	if pricing == nil || factor == 1 {
		return pricing
	}
	cloned := *pricing
	scale := func(v float64) float64 {
		if v == 0 {
			return 0
		}
		return v * factor
	}
	cloned.InputPricePerToken = scale(cloned.InputPricePerToken)
	cloned.InputPricePerTokenPriority = scale(cloned.InputPricePerTokenPriority)
	cloned.ImageInputPricePerToken = scale(cloned.ImageInputPricePerToken)
	cloned.OutputPricePerToken = scale(cloned.OutputPricePerToken)
	cloned.OutputPricePerTokenPriority = scale(cloned.OutputPricePerTokenPriority)
	cloned.ThinkingOutputPricePerToken = scale(cloned.ThinkingOutputPricePerToken)
	cloned.CacheCreationPricePerToken = scale(cloned.CacheCreationPricePerToken)
	cloned.CacheCreationPricePerTokenPriority = scale(cloned.CacheCreationPricePerTokenPriority)
	cloned.CacheReadPricePerToken = scale(cloned.CacheReadPricePerToken)
	cloned.CacheReadPricePerTokenPriority = scale(cloned.CacheReadPricePerTokenPriority)
	cloned.CacheCreation5mPrice = scale(cloned.CacheCreation5mPrice)
	cloned.CacheCreation1hPrice = scale(cloned.CacheCreation1hPrice)
	cloned.ImageOutputPricePerToken = scale(cloned.ImageOutputPricePerToken)
	if len(cloned.Intervals) > 0 {
		intervals := make([]PricingInterval, len(cloned.Intervals))
		copy(intervals, cloned.Intervals)
		for i := range intervals {
			if intervals[i].InputPrice != nil {
				v := scale(*intervals[i].InputPrice)
				intervals[i].InputPrice = &v
			}
			if intervals[i].OutputPrice != nil {
				v := scale(*intervals[i].OutputPrice)
				intervals[i].OutputPrice = &v
			}
			if intervals[i].CacheWritePrice != nil {
				v := scale(*intervals[i].CacheWritePrice)
				intervals[i].CacheWritePrice = &v
			}
			if intervals[i].CacheReadPrice != nil {
				v := scale(*intervals[i].CacheReadPrice)
				intervals[i].CacheReadPrice = &v
			}
		}
		cloned.Intervals = intervals
	}
	return &cloned
}
