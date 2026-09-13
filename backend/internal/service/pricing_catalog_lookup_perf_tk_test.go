//go:build unit

package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCatalogMembershipIndexKeepsOriginalScanOrder(t *testing.T) {
	models := []PublicCatalogModel{
		{ModelID: "gpt-example-2026", Vendor: "openai"},
		{ModelID: "gpt-example", Vendor: "openai"},
		{ModelID: "gpt-example-2025", Vendor: "openai"},
		{ModelID: "hidden-family", Vendor: "deepseek"},
		{ModelID: "hidden-family-2026", Vendor: "openai"},
		{ModelID: "gpt-example", Vendor: "openai"},
	}
	index := buildCatalogMembershipIndex(models)
	require.Equal(t, 1, index.literal["gpt-example"], "literal exact match still precedes alias fallback")
	require.Equal(t, 0, index.fallback["gpt-example"], "alias fallback preserves first matching row, not a new exact-first order")
	require.Equal(t, 3, index.literal["hidden-family"], "hidden literal row must remain a rejection, not fall through to an alias")
	require.Equal(t, 4, index.fallback["hidden-family"], "alias scan skips hidden rows")
	_, familyLeak := index.fallback["gpt"]
	require.False(t, familyLeak)
	_, hiddenListed := index.fallback["hidden-family-unknown"]
	require.False(t, hiddenListed)
}

func TestCatalogMembershipIndexRotatesWithRegistryAndFile(t *testing.T) {
	tkOverlayMu.Lock()
	previous := tkOverlayEffective
	tkOverlayMu.Unlock()
	t.Cleanup(func() {
		tkOverlayMu.Lock()
		tkOverlayEffective = previous
		tkOverlayMu.Unlock()
	})
	setRegistry := func(price float64) {
		tkOverlayMu.Lock()
		tkOverlayEffective = &tkPricingOverlaySnapshot{Models: map[string]*LiteLLMModelPricing{
			"gpt-5.4": {InputCostPerToken: price, OutputCostPerToken: price * 4, LiteLLMProvider: "openai", Mode: "chat"},
		}}
		tkOverlayMu.Unlock()
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "model_pricing.json")
	require.NoError(t, os.WriteFile(file, []byte(`{"gpt-5.4":{"input_cost_per_token":0.99,"litellm_provider":"openai"}}`), 0600))
	cfg := &config.Config{}
	cfg.Pricing.DataDir = dir
	svc := NewPricingCatalogService(cfg)
	setRegistry(1e-6)
	old, ok := svc.findCatalogModel("gpt-5.4")
	require.True(t, ok)
	require.InDelta(t, 0.001, old.Pricing.InputPer1KTokens, 1e-15)
	setRegistry(2e-6)
	newModel, ok := svc.findCatalogModel("gpt-5.4")
	require.True(t, ok)
	require.NotSame(t, old, newModel)
	require.InDelta(t, 0.002, newModel.Pricing.InputPer1KTokens, 1e-15)

	require.NoError(t, os.WriteFile(file, []byte(`not-json`), 0600))
	changed := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(file, changed, changed))
	require.True(t, svc.IsModelPriced("gpt-5.4", "openai"), "malformed sensor must not retire registry membership")
	require.NoError(t, os.Remove(file))
	require.True(t, svc.IsModelPriced("gpt-5.4", "openai"))
}
