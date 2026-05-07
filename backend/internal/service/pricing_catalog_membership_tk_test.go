//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Tests for pricing_catalog_membership_tk.go (IsModelPriced).
//
// These predicates are load-bearing for the upstream-discovery filter (Goal 1)
// and the client model-list filter (Goal 2). Low-severity review finding R-003
// from docs/review-20260507 requested isolation tests.

func TestIsModelPriced_NilReceiver(t *testing.T) {
	var s *PricingCatalogService
	require.False(t, s.IsModelPriced("claude-3-opus-20240229", "anthropic"),
		"nil receiver must return false (fail-open, not panic)")
}

func TestIsModelPriced_EmptyModelID(t *testing.T) {
	svc := NewPricingCatalogService(nil)
	require.False(t, svc.IsModelPriced("", "anthropic"),
		"empty modelID must return false")
	require.False(t, svc.IsModelPriced("   ", "anthropic"),
		"whitespace-only modelID must return false")
}

func TestIsModelPriced_ColdCatalog(t *testing.T) {
	// nil config → defaultCatalogSource → ok=false → empty catalog
	svc := NewPricingCatalogService(nil)
	require.False(t, svc.IsModelPriced("claude-3-opus-20240229", "anthropic"),
		"cold catalog (no data source) must return false")
}

func TestIsModelPriced_WithFixtureData(t *testing.T) {
	svc := NewPricingCatalogService(nil)
	svc.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(`{
			"claude-3-opus-20240229": {
				"input_cost_per_token": 0.000015,
				"output_cost_per_token": 0.000075,
				"litellm_provider": "anthropic"
			},
			"gpt-4o": {
				"input_cost_per_token": 0.000005,
				"output_cost_per_token": 0.000015,
				"litellm_provider": "openai"
			}
		}`), time.Now(), true
	})

	require.True(t, svc.IsModelPriced("claude-3-opus-20240229", "anthropic"))
	require.True(t, svc.IsModelPriced("gpt-4o", "openai"))
	// Platform parameter is currently ignored (catalog is platform-agnostic v1)
	require.True(t, svc.IsModelPriced("claude-3-opus-20240229", ""))
	require.False(t, svc.IsModelPriced("gpt-5-unknown", "openai"),
		"model absent from catalog must return false")
}

