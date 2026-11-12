//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fable 5.1 and 5.2 must both appear on the public /pricing catalog with their
// own official price cards.
//
// Regression anchor: 5.2 was priced and served but never added to the catalog
// allowlists, so the admin account screen showed "缺定价" and the model was
// absent from the marketplace. 5.1 had the same gap for longer even though it
// carries live prod traffic — the catalog only ever advertised bare
// claude-fable-5.
func TestPublicCatalog_ExposesFable51And52WithOwnPrices(t *testing.T) {
	t.Parallel()

	resp := FilterPublicCatalogToServable((&PricingCatalogService{}).BuildPublicCatalog(context.Background()))
	require.NotNil(t, resp)

	byID := make(map[string]PublicCatalogModel, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ModelID] = m
	}

	const epsilon = 1e-9
	cases := []struct {
		model      string
		inPer1K    float64 // USD per 1K tokens
		outPer1K   float64
		cacheRead  float64
		cacheWrite float64
	}{
		// Fable 5.1 official: $10/$50 per M, cache read $0.25/M, 5m write $12.50/M.
		{model: "claude-fable-5-1", inPer1K: 0.01, outPer1K: 0.05, cacheRead: 0.00025, cacheWrite: 0.0125},
		// Fable 5.2 official: $8/$40 per M, cache read $0.20/M, 5m write $10/M.
		{model: "claude-fable-5-2", inPer1K: 0.008, outPer1K: 0.04, cacheRead: 0.0002, cacheWrite: 0.01},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			m, ok := byID[tc.model]
			require.True(t, ok, "%s must appear on the public catalog", tc.model)
			assert.Equal(t, "anthropic", m.Vendor, "%s keeps OEM vendor", tc.model)

			assert.InDelta(t, tc.inPer1K, m.Pricing.InputPer1KTokens, epsilon, "%s input price", tc.model)
			assert.InDelta(t, tc.outPer1K, m.Pricing.OutputPer1KTokens, epsilon, "%s output price", tc.model)
			assert.InDelta(t, tc.cacheRead, m.Pricing.CacheReadPer1K, epsilon, "%s cache read", tc.model)
			assert.InDelta(t, tc.cacheWrite, m.Pricing.CacheWritePer1K, epsilon, "%s cache write", tc.model)
		})
	}
}

// The tokensea anthropic relay floor drives provisioning and the wire mapping;
// both Fable revisions must stay in it so a new account gets them by default.
func TestAnthropicTokenseaRelayFloor_IncludesBothFableRevisions(t *testing.T) {
	floor := anthropicTokenseaRelayModelMappingFloor()
	require.NotEmpty(t, floor)
	for _, id := range []string{"claude-fable-5-1", "claude-fable-5-2"} {
		require.Contains(t, floor, id, "tokensea relay floor must provision %s", id)
	}
}
