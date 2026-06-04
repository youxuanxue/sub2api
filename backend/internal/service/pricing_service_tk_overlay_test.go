package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTKPricingOverlay_FillsDeepseekV4 verifies the overlay supplies pricing for
// text models the trimmed runtime source lacks (deepseek-v4-*), so they no longer
// bill $0 via pricing_missing_record_zero_cost. The source body deliberately
// carries no deepseek key — mirroring the Wei-Shaw mirror as of 2026-06-04.
func TestTKPricingOverlay_FillsDeepseekV4(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"output_cost_per_token": 0.000015,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	flash := data["deepseek-v4-flash"]
	require.NotNil(t, flash, "overlay must inject deepseek-v4-flash")
	require.InDelta(t, 1.4e-7, flash.InputCostPerToken, 1e-15)
	require.InDelta(t, 2.8e-7, flash.OutputCostPerToken, 1e-15)
	require.InDelta(t, 2.8e-9, flash.CacheReadInputTokenCost, 1e-15)
	require.True(t, flash.SupportsPromptCaching)
	require.Equal(t, "deepseek", flash.LiteLLMProvider)
	require.Equal(t, "chat", flash.Mode)

	pro := data["deepseek-v4-pro"]
	require.NotNil(t, pro, "overlay must inject deepseek-v4-pro")
	require.InDelta(t, 4.35e-7, pro.InputCostPerToken, 1e-15)
	require.InDelta(t, 8.7e-7, pro.OutputCostPerToken, 1e-15)
	require.InDelta(t, 3.625e-9, pro.CacheReadInputTokenCost, 1e-15)
}

// TestTKPricingOverlay_FillOnlySourceWins verifies the overlay never overwrites
// an entry the loaded source already carries: the day the mirror catalogues
// deepseek-v4-flash natively, the source value must win (self-deprecating).
func TestTKPricingOverlay_FillOnlySourceWins(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"deepseek-v4-flash": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002,
			"litellm_provider": "deepseek",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	flash := data["deepseek-v4-flash"]
	require.NotNil(t, flash)
	require.InDelta(t, 1e-6, flash.InputCostPerToken, 1e-15, "source value must win over overlay")
	require.InDelta(t, 2e-6, flash.OutputCostPerToken, 1e-15, "source value must win over overlay")
}

// TestTKPricingOverlay_MediaEntriesStillPresent guards the original media scope
// through the media→generic rename: imagen/veo per-image and per-second prices
// must keep flowing from the renamed embed.
func TestTKPricingOverlay_MediaEntriesStillPresent(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {"input_cost_per_token": 0.0000025, "output_cost_per_token": 0.000015, "litellm_provider": "openai", "mode": "chat"}
	}`))
	require.NoError(t, err)

	imagen := data["imagen-4.0-generate-001"]
	require.NotNil(t, imagen, "media overlay entry imagen-4.0-generate-001 must survive the rename")
	require.InDelta(t, 0.04, imagen.OutputCostPerImage, 1e-12)

	veo := data["veo-3.0-generate-001"]
	require.NotNil(t, veo, "media overlay entry veo-3.0-generate-001 must survive the rename")
	require.InDelta(t, 0.4, veo.OutputCostPerSecond, 1e-12)
}
