package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
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

func TestTKPricingOverlay_FillsMoonshotChinaModels(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {"input_cost_per_token": 0.0000025, "output_cost_per_token": 0.000015, "litellm_provider": "openai", "mode": "chat"}
	}`))
	require.NoError(t, err)

	for _, modelID := range tkServedModelsManifestPresetIDsByChannelType(25) {
		pricing := data[modelID]
		require.NotNil(t, pricing, "overlay must price manifest-owned Moonshot model %s", modelID)
		require.False(t, tkIsEffectivelyUnpriced(pricing), "Moonshot model %s must never resolve to a zero price", modelID)
		require.Equal(t, "moonshot", pricing.LiteLLMProvider, modelID)
	}

	auto := data["moonshot-v1-auto"]
	v128 := data["moonshot-v1-128k"]
	require.NotNil(t, auto)
	require.NotNil(t, v128)
	require.InDelta(t, v128.InputCostPerToken, auto.InputCostPerToken, 1e-15,
		"operator decision: moonshot-v1-auto is fixed to the 128K input tier")
	require.InDelta(t, v128.OutputCostPerToken, auto.OutputCostPerToken, 1e-15,
		"operator decision: moonshot-v1-auto is fixed to the 128K output tier")
	require.Empty(t, auto.Intervals, "moonshot-v1-auto must remain fixed-price, not input-tiered")

	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	k3, err := billing.GetModelPricing("kimi-k3")
	require.NoError(t, err)
	require.InDelta(t, data["kimi-k3"].InputCostPerToken*tkOfficialListBaseTaxMultiplier, k3.InputPricePerToken, 1e-15)
	require.InDelta(t, data["kimi-k3"].OutputCostPerToken*tkOfficialListBaseTaxMultiplier, k3.OutputPricePerToken, 1e-15)
	require.InDelta(t, data["kimi-k3"].CacheReadInputTokenCost*tkOfficialListBaseTaxMultiplier, k3.CacheReadPricePerToken, 1e-15)
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

// TestTKPricingOverlay_FillsAntigravityGeminiThinking verifies the Antigravity
// Gemini thinking wire id has a concrete price. The plain gemini-2.5-flash row
// is already present in the bundled/runtime Gemini catalog; the Antigravity
// suffix id is not, so without this overlay it would be servable but $0-priced.
func TestTKPricingOverlay_FillsAntigravityGeminiThinking(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"gemini-2.5-flash": {
			"input_cost_per_token": 0.0000003,
			"output_cost_per_token": 0.0000025,
			"cache_read_input_token_cost": 0.00000003,
			"litellm_provider": "vertex_ai-language-models",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	thinking := data["gemini-2.5-flash-thinking"]
	require.NotNil(t, thinking, "overlay must inject Antigravity gemini-2.5-flash-thinking")
	require.Equal(t, "antigravity", thinking.LiteLLMProvider)
	require.Equal(t, "chat", thinking.Mode)
	require.InDelta(t, 3e-7, thinking.InputCostPerToken, 1e-15)
	require.InDelta(t, 2.5e-6, thinking.OutputCostPerToken, 1e-15)
	require.InDelta(t, 3e-8, thinking.CacheReadInputTokenCost, 1e-15)
	require.True(t, thinking.SupportsPromptCaching)
}

// TestTKPricingOverlay_FillsGeminiProAgent verifies the Antigravity Gemini 3.1 Pro
// High wire id (gemini-pro-agent) has a concrete price. Without this overlay entry the
// model was servable but billed $0 (upstream Wei-Shaw/sub2api#2486).
func TestTKPricingOverlay_FillsGeminiProAgent(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"gemini-2.5-pro": {
			"input_cost_per_token": 0.00000125,
			"output_cost_per_token": 0.00001,
			"cache_read_input_token_cost": 0.000000125,
			"litellm_provider": "vertex_ai-language-models",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	proAgent := data["gemini-pro-agent"]
	require.NotNil(t, proAgent, "overlay must inject gemini-pro-agent")
	require.Equal(t, "antigravity", proAgent.LiteLLMProvider)
	require.Equal(t, "chat", proAgent.Mode)
	require.InDelta(t, 2e-6, proAgent.InputCostPerToken, 1e-15)
	require.InDelta(t, 1.2e-5, proAgent.OutputCostPerToken, 1e-15)
	require.InDelta(t, 2e-7, proAgent.CacheReadInputTokenCost, 1e-15)
}

// TestTKPricingOverlay_ZeroPlaceholderIsReplaced verifies the absent-or-zero fill:
// a source entry whose every cost field is 0.0 (litellm's "cost unknown" shape)
// must NOT shadow a curated overlay price for a manifest-listed model.
func TestTKPricingOverlay_ZeroPlaceholderIsReplaced(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"deepseek-v4-pro": {
			"input_cost_per_token": 0.0,
			"output_cost_per_token": 0.0,
			"litellm_provider": "deepseek",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	pro := data["deepseek-v4-pro"]
	require.NotNil(t, pro)
	require.InDelta(t, 4.35e-7, pro.InputCostPerToken, 1e-15,
		"zero placeholder must be replaced by the overlay deepseek-v4-pro price")
	require.InDelta(t, 8.7e-7, pro.OutputCostPerToken, 1e-15)
	require.InDelta(t, 3.625e-9, pro.CacheReadInputTokenCost, 1e-15)
}

func TestApplyTKPricingOverlay_GLMAuthoritativeOverLitellm(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"glm-5.2": {
			"input_cost_per_token": 1.4e-06,
			"output_cost_per_token": 4.4e-06,
			"cache_read_input_token_cost": 2.6e-07,
			"litellm_provider": "zhipu",
			"mode": "chat"
		}
	}`)
	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	glm := data["glm-5.2"]
	require.NotNil(t, glm)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(8), glm.InputCostPerToken, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(28), glm.OutputCostPerToken, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(2), glm.CacheReadInputTokenCost, 1e-15)
}

// TestTKIsEffectivelyUnpriced pins the predicate: zero-everything (and nil) are
// unpriced; any single non-zero cost field — token, cache, or media — counts as
// priced, so media entries (per-image / per-second only) are never mistaken for
// placeholders.
func TestTKIsEffectivelyUnpriced(t *testing.T) {
	require.True(t, tkIsEffectivelyUnpriced(nil))
	require.True(t, tkIsEffectivelyUnpriced(&LiteLLMModelPricing{LiteLLMProvider: "volcengine", Mode: "chat"}))

	require.False(t, tkIsEffectivelyUnpriced(&LiteLLMModelPricing{InputCostPerToken: 1e-7}))
	require.False(t, tkIsEffectivelyUnpriced(&LiteLLMModelPricing{CacheReadInputTokenCost: 1e-9}))
	require.False(t, tkIsEffectivelyUnpriced(&LiteLLMModelPricing{OutputCostPerImage: 0.04}), "per-image-only media entry is priced")
	require.False(t, tkIsEffectivelyUnpriced(&LiteLLMModelPricing{OutputCostPerSecond: 0.4}), "per-second-only media entry is priced")
}

// TestBilling_ZeroPlaceholderFallsToPricingMissing verifies the billing-side use
// of the same predicate: a zero-placeholder entry for a model with no overlay
// entry and no hardcoded fallback must surface ErrModelPricingUnavailable (the
// existing zero-cost + Feishu pricing-missing funnel), not silently return $0
// prices as a successful lookup.
func TestBilling_ZeroPlaceholderFallsToPricingMissing(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"some-future-model-not-curated": {
			"input_cost_per_token": 0.0,
			"output_cost_per_token": 0.0,
			"litellm_provider": "volcengine",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	_, err = billing.GetModelPricing("some-future-model-not-curated")
	require.ErrorIs(t, err, ErrModelPricingUnavailable,
		"zero placeholder must be treated as pricing-missing, not a $0 price")
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

// TestTKPricingOverlay_SeedMediaEntries guards the VolcEngine seedream (per-image)
// and seedance (per-second) overlay prices: the runtime mirror is a trimmed litellm
// that has no media entries for these IDs, so without the overlay every seedream
// image bills $0 and every seedance second bills $0.
func TestTKPricingOverlay_SeedMediaEntries(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {"input_cost_per_token": 0.0000025, "output_cost_per_token": 0.000015, "litellm_provider": "openai", "mode": "chat"}
	}`))
	require.NoError(t, err)
	for model, wantPerImage := range map[string]float64{
		"doubao-seedream-4-0-250828": 0.029850746268656716,
		"doubao-seedream-4-5-251128": 0.03731343283582089,
		"doubao-seedream-5-0-260128": 0.03283582089552239,
		"seedream-4-0-250828":        0.029850746268656716,
	} {
		entry := data[model]
		require.NotNil(t, entry, "overlay must carry %s", model)
		require.InDelta(t, wantPerImage, entry.OutputCostPerImage, 1e-12, model)
	}
	for model, wantPerSecond := range map[string]float64{
		"doubao-seedance-1-0-pro-250528":      0.10880597014925374,
		"seedance-1-0-pro-250528":             0.10880597014925374,
		"doubao-seedance-1-0-pro-fast-251015": 0.030465671641791044,
		"doubao-seedance-1-5-pro-251215":      0.11611940298507463,
		"doubao-seedance-2-0-260128":          0.36985074626865673,
		"doubao-seedance-2-0-fast-260128":     0.11940298507462686,
	} {
		entry := data[model]
		require.NotNil(t, entry, "overlay must carry %s", model)
		require.InDelta(t, wantPerSecond, entry.OutputCostPerSecond, 1e-12, model)
	}
}

// TestTKPricingOverlay_CopiesCacheCreation1hPrice guards the overlay loader's
// field copy of cache_creation_input_token_cost_above_1hr. The loader used to
// drop it (only the main parsePricingData entry path copied it), so any overlay
// model with a 1h cache-write tier — claude-fable-5 — silently billed 1h cache
// creation at the 5m rate ($12.50/MTok instead of $20/MTok).
func TestTKPricingOverlay_CopiesCacheCreation1hPrice(t *testing.T) {
	overlay := loadTKPricingOverlay()
	fable := overlay["claude-fable-5"]
	require.NotNil(t, fable, "overlay must carry claude-fable-5")
	require.InDelta(t, 1.25e-5, fable.CacheCreationInputTokenCost, 1e-15)
	require.InDelta(t, 2e-5, fable.CacheCreationInputTokenCostAbove1hr, 1e-15,
		"overlay loader must copy the 1h cache-write price")
}

// TestBilling_FableOverlayEnablesCacheBreakdown verifies end-to-end that a
// claude-fable-5 pricing resolved via the TK overlay (the model is absent from
// the trimmed runtime source) enables 5m/1h cache breakdown billing:
// price1h (2e-5) > price5m (1.25e-5) > 0.
func TestBilling_FableOverlayEnablesCacheBreakdown(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {"input_cost_per_token": 0.0000025, "output_cost_per_token": 0.000015, "litellm_provider": "openai", "mode": "chat"}
	}`))
	require.NoError(t, err)
	require.NotNil(t, data["claude-fable-5"], "fable must be injected by the overlay (absent from source body)")

	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	pricing, err := billing.GetModelPricing("claude-fable-5")
	require.NoError(t, err)
	require.True(t, pricing.SupportsCacheBreakdown, "1h > 5m price must enable breakdown")
	require.InDelta(t, 1.25e-5, pricing.CacheCreation5mPrice, 1e-15)
	require.InDelta(t, 2e-5, pricing.CacheCreation1hPrice, 1e-15)
}

// TestBilling_Fable1hCacheCreationCost_ProdShape is the regression reproduction
// with the exact prod token shape that exposed the bug (usage_logs, 2026-06):
// cache_creation_5m_tokens=0, cache_creation_1h_tokens=684124. Correct cost is
// 684124 * 2e-5 = $13.68248; before the fix the overlay dropped the 1h price,
// breakdown stayed off, and the same shape billed flat 5m rate:
// 684124 * 1.25e-5 = $8.55155.
func TestBilling_Fable1hCacheCreationCost_ProdShape(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {"input_cost_per_token": 0.0000025, "output_cost_per_token": 0.000015, "litellm_provider": "openai", "mode": "chat"}
	}`))
	require.NoError(t, err)

	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	tokens := UsageTokens{
		CacheCreationTokens:   684124,
		CacheCreation5mTokens: 0,
		CacheCreation1hTokens: 684124,
	}
	breakdown, err := billing.CalculateCost("claude-fable-5", tokens, 1.0)
	require.NoError(t, err)
	require.InDelta(t, 13.68248, breakdown.CacheCreationCost, 1e-6)
}

// TestBilling_SourceCarried1hPriceUnaffected is the no-regression control: a
// model whose 1h cache-write price comes from the runtime source (main
// parsePricingData path, which always copied the field) must bill exactly as
// before the overlay-loader fix.
func TestBilling_SourceCarried1hPriceUnaffected(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"claude-opus-4-6": {
			"input_cost_per_token": 5e-06,
			"output_cost_per_token": 2.5e-05,
			"cache_creation_input_token_cost": 6.25e-06,
			"cache_creation_input_token_cost_above_1hr": 1e-05,
			"cache_read_input_token_cost": 5e-07,
			"litellm_provider": "anthropic",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	pricing, err := billing.GetModelPricing("claude-opus-4-6")
	require.NoError(t, err)
	require.True(t, pricing.SupportsCacheBreakdown)
	require.InDelta(t, 6.25e-6, pricing.CacheCreation5mPrice, 1e-15)
	require.InDelta(t, 1e-5, pricing.CacheCreation1hPrice, 1e-15)

	breakdown, err := billing.CalculateCost("claude-opus-4-6", UsageTokens{
		CacheCreationTokens:   1000000,
		CacheCreation5mTokens: 400000,
		CacheCreation1hTokens: 600000,
	}, 1.0)
	require.NoError(t, err)
	// 400000*6.25e-6 + 600000*1e-5 = 2.5 + 6.0
	require.InDelta(t, 8.5, breakdown.CacheCreationCost, 1e-9)
}

// TestTKPricingOverlay_FillsRetiredQwen25Coder verifies the overlay supplies
// pricing for qwen2.5-coder-32b / qwen2.5-coder-7b — retired models absent from
// both the runtime price source AND Alibaba's current price list, proxy-filled
// from lineage successors qwen3-coder-plus / qwen3-coder-flash (Beijing RMB list
// ÷ 6.7). Before this fill they billed $0: in 2026-04/05 a trajectory harness
// served 66,556 requests (mapped to gpt-5.4) at $0 because the unpriced requested
// name was the billing key. The fill makes both names carry a non-zero,
// input-tiered price so they no longer silently leak via pricing-missing $0.
func TestTKPricingOverlay_FillsRetiredQwen25Coder(t *testing.T) {
	svc := &PricingService{}
	// Source deliberately carries neither qwen2.5-coder key (mirrors prod, where
	// the trimmed source lacks them), so the overlay fill must apply.
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

	c32 := data["qwen2.5-coder-32b"]
	require.NotNil(t, c32, "overlay must inject qwen2.5-coder-32b")
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(4), c32.InputCostPerToken, 1e-13)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(16), c32.OutputCostPerToken, 1e-13)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(4), c32.CacheReadInputTokenCost, 1e-13)
	require.Len(t, c32.Intervals, 4, "32b must carry the 4 input-token tiers")
	top32 := c32.Intervals[len(c32.Intervals)-1]
	require.Nil(t, top32.MaxTokens, "last 32b tier is unbounded (>256K)")
	require.NotNil(t, top32.InputPrice)
	require.NotNil(t, top32.OutputPrice)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(20), *top32.InputPrice, 1e-13)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(200), *top32.OutputPrice, 1e-13)

	c7 := data["qwen2.5-coder-7b"]
	require.NotNil(t, c7, "overlay must inject qwen2.5-coder-7b")
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(1), c7.InputCostPerToken, 1e-13)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(4), c7.OutputCostPerToken, 1e-13)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(1), c7.CacheReadInputTokenCost, 1e-13)
	require.Len(t, c7.Intervals, 4, "7b must carry the 4 input-token tiers")
	base7 := c7.Intervals[0]
	require.NotNil(t, base7.InputPrice)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(1), *base7.InputPrice, 1e-13)
}
