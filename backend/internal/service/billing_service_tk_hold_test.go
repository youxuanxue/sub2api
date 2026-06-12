//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// The load-bearing property of the overdraft fix: the hold estimate must be an
// UPPER BOUND on whatever the request can actually be billed. If any reachable
// token distribution (input ≤ prompt, output ≤ max_tokens) costs more than the
// hold, the "balance never goes negative" guarantee is void. These tests pin
// that property across token splits and service tiers.

func TestEstimateTokenHold_IsUpperBoundOverDistributions(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil) // fallback pricing, no pricingService
	const (
		model   = "claude-sonnet-4"
		prompt  = 1000 // upper bound on input tokens
		maxOut  = 500  // hard output ceiling
		mult    = 1.0
		epsilon = 1e-9
	)

	for _, tier := range []string{"", "priority", "flex"} {
		hold, err := s.EstimateTokenHold(model, tier, prompt, maxOut, mult)
		if err != nil {
			t.Fatalf("EstimateTokenHold(tier=%q): %v", tier, err)
		}

		// Every distribution the request could actually resolve to: input is
		// split across input / cache-creation / cache-read (cache-creation is
		// the dearest, cache-read the cheapest), output up to maxOut.
		dists := []UsageTokens{
			{InputTokens: prompt, OutputTokens: maxOut},
			{CacheCreationTokens: prompt, OutputTokens: maxOut}, // most expensive input
			{InputTokens: prompt / 2, CacheCreationTokens: prompt / 2, OutputTokens: maxOut},
			{CacheReadTokens: prompt, OutputTokens: maxOut}, // cheapest input
			{InputTokens: prompt, OutputTokens: 0},
		}
		for _, d := range dists {
			bd, err := s.CalculateCostWithServiceTier(model, d, mult, tier)
			if err != nil {
				t.Fatalf("CalculateCostWithServiceTier(tier=%q, %+v): %v", tier, d, err)
			}
			if bd.ActualCost > hold+epsilon {
				t.Errorf("hold is NOT an upper bound: tier=%q dist=%+v actual=%.12f > hold=%.12f",
					tier, d, bd.ActualCost, hold)
			}
		}
	}
}

func TestEstimateTokenHold_ScalesWithRateMultiplier(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	h1, err := s.EstimateTokenHold("claude-sonnet-4", "", 1000, 500, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.EstimateTokenHold("claude-sonnet-4", "", 1000, 500, 2.0)
	if err != nil {
		t.Fatal(err)
	}
	if h2 <= h1 {
		t.Errorf("hold should scale with rate multiplier: mult=1 → %.10f, mult=2 → %.10f", h1, h2)
	}
}

func TestEstimateTokenHold_UnpricedModelErrors(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	if _, err := s.EstimateTokenHold("definitely-not-a-real-model-xyz", "", 100, 100, 1.0); err == nil {
		t.Error("expected an error for an unpriced model so the caller can fail-open (chat serves $0)")
	}
}

func TestEstimateImageHold_CoversFewerDeliveredImages(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	// Reserve for the requested count; actual delivers ≤ n, so hold ≥ actual.
	hold := s.EstimateImageHold("some-image-model", "2K", 4, nil, 1.0)
	actual := s.CalculateImageCost("some-image-model", "2K", 2, nil, 1.0).ActualCost
	if actual > hold {
		t.Errorf("image hold (n=4) must cover actual fewer images (n=2): hold=%.6f actual=%.6f", hold, actual)
	}
	// An omitted size tier must be priced as the dearest tier (4K), never under.
	holdEmpty := s.EstimateImageHold("some-image-model", "", 1, nil, 1.0)
	hold4K := s.EstimateImageHold("some-image-model", "4K", 1, nil, 1.0)
	if holdEmpty < hold4K {
		t.Errorf("empty size tier must reserve as 4K: empty=%.6f 4K=%.6f", holdEmpty, hold4K)
	}
}

func TestEstimateVideoHold_MatchesBilledDuration(t *testing.T) {
	s := NewBillingService(&config.Config{}, nil)
	hold := s.EstimateVideoHold("some-video-model", 8, 1.0)
	actual := s.CalculateVideoCost("some-video-model", 8, 1.0).ActualCost
	if hold < actual {
		t.Errorf("video hold must be ≥ billed cost for the same duration: hold=%.6f actual=%.6f", hold, actual)
	}
}

func TestMaxFloat(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{nil, 0},
		{[]float64{1, 2, 3}, 3},
		{[]float64{-1, -2}, 0}, // never below zero
		{[]float64{0.3e-6, 3.75e-6, 3e-6}, 3.75e-6},
	}
	for _, c := range cases {
		if got := maxFloat(c.in...); got != c.want {
			t.Errorf("maxFloat(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
