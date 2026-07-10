package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestParsePricingData_ParsesPriorityAndServiceTierFields(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_creation_input_token_cost": 0.0000025,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"supports_prompt_caching": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)
	pricing := data["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 3e-5, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 5e-7, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_KeepsImageOnlyPricing(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"image-only-model": {
			"output_cost_per_image": 0.034,
			"litellm_provider": "vertex_ai-language-models",
			"mode": "image_generation"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)
	pricing := data["image-only-model"]
	require.NotNil(t, pricing)
	require.InDelta(t, 0.034, pricing.OutputCostPerImage, 1e-12)
	require.Equal(t, "image_generation", pricing.Mode)
	// 仅有图片价的条目必须标记 token 价缺失，供 token 计费路径 fail-closed。
	require.True(t, pricing.TokenPricingAbsent)
}

func TestBillingService_GetModelPricing_FailsClosedForImageOnlyEntries(t *testing.T) {
	pricingSvc := &PricingService{}
	data, err := pricingSvc.parsePricingData([]byte(`{
		"imagen-9.0-generate": {
			"output_cost_per_image": 0.04,
			"litellm_provider": "vertex_ai-image-models",
			"mode": "image_generation"
		},
		"gemini-image-with-token-price": {
			"input_cost_per_token": 0.0,
			"output_cost_per_token": 0.0,
			"output_cost_per_image": 0.034,
			"litellm_provider": "vertex_ai-language-models",
			"mode": "image_generation"
		}
	}`))
	require.NoError(t, err)
	pricingSvc.pricingData = data
	billingSvc := NewBillingService(&config.Config{}, pricingSvc)

	// image-only 条目不得进入 token 计费（否则 token 流量按 $0 计费），
	// 必须落到 fallback / ErrModelPricingUnavailable 的 fail-closed 路径。
	_, err = billingSvc.GetModelPricing("imagen-9.0-generate")
	require.ErrorIs(t, err, ErrModelPricingUnavailable)

	// 显式 0 token 价的免费条目保持历史行为：正常返回。
	pricing, err := billingSvc.GetModelPricing("gemini-image-with-token-price")
	require.NoError(t, err)
	require.Zero(t, pricing.InputPricePerToken)

	// 图片计费路径不受影响：仍能读到 image-only 条目的图片单价。
	raw := pricingSvc.GetModelPricing("imagen-9.0-generate")
	require.NotNil(t, raw)
	require.InDelta(t, 0.04, raw.OutputCostPerImage, 1e-12)
}

func TestPricingService_MergesFallbackOnlyModels(t *testing.T) {
	dir := t.TempDir()
	fallbackFile := filepath.Join(dir, "fallback.json")
	require.NoError(t, os.WriteFile(fallbackFile, []byte(`{
		"remote-model": {
			"input_cost_per_token": 0.000001,
			"litellm_provider": "test",
			"mode": "chat"
		},
		"gemini-3.1-flash-lite-image": {
			"output_cost_per_image": 0.034,
			"litellm_provider": "vertex_ai-language-models",
			"mode": "image_generation"
		}
	}`), 0644))

	svc := &PricingService{cfg: &config.Config{}}
	svc.cfg.Pricing.FallbackFile = fallbackFile
	remoteData, err := svc.parsePricingData([]byte(`{
		"remote-model": {
			"input_cost_per_token": 0.000002,
			"litellm_provider": "test",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	merged := svc.mergeFallbackPricingData(remoteData)
	require.InDelta(t, 0.000002, merged["remote-model"].InputCostPerToken, 1e-12)
	require.NotNil(t, merged["gemini-3.1-flash-lite-image"])
	require.InDelta(t, 0.034, merged["gemini-3.1-flash-lite-image"].OutputCostPerImage, 1e-12)
}

func TestGetModelPricing_Gpt53CodexSparkUsesDedicatedSparkPricing(t *testing.T) {
	sparkPricing := &LiteLLMModelPricing{InputCostPerToken: 1}
	gpt53Pricing := &LiteLLMModelPricing{InputCostPerToken: 9}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.3-codex-spark": sparkPricing,
			"gpt-5.3":             gpt53Pricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex-spark")
	require.Same(t, sparkPricing, got)
}

func TestGetModelPricing_Gpt53CodexFallbackUsesSpark(t *testing.T) {
	sparkPricing := &LiteLLMModelPricing{InputCostPerToken: 2}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.3-codex-spark": sparkPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, sparkPricing, got)
}

func TestGetModelPricing_Gpt5CodexFallbackUsesSpark(t *testing.T) {
	sparkPricing := &LiteLLMModelPricing{InputCostPerToken: 2}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.3-codex-spark": sparkPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5-codex")
	require.Same(t, sparkPricing, got)
}

func TestCalculateCost_ClaudeDotFormMatchesDashFormFamilyPricing(t *testing.T) {
	sonnetPricing := &LiteLLMModelPricing{InputCostPerToken: 3e-6}
	opusPricing := &LiteLLMModelPricing{InputCostPerToken: 5e-6}

	pricing := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-sonnet-4-5-20250929": sonnetPricing,
			"claude-opus-4-5-20251101":   opusPricing,
		},
	}
	billing := NewBillingService(nil, pricing)

	sonnetCost, err := billing.CalculateCost("claude-sonnet-4.5", UsageTokens{InputTokens: 1000}, 1.0)
	require.NoError(t, err)
	require.InDelta(t, 0.003, sonnetCost.TotalCost, 1e-12)

	opusCost, err := billing.CalculateCost("claude-opus-4.5", UsageTokens{InputTokens: 1000}, 1.0)
	require.NoError(t, err)
	require.InDelta(t, 0.005, opusCost.TotalCost, 1e-12)
}

func TestGetModelPricing_OpenAIFallbackMatchedLoggedAsInfo(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	sparkPricing := &LiteLLMModelPricing{InputCostPerToken: 2}
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.3-codex-spark": sparkPricing,
		},
	}

	got := svc.GetModelPricing("gpt-5.3-codex")
	require.Same(t, sparkPricing, got)

	require.True(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.3-codex-spark", "info"))
	require.False(t, logSink.ContainsMessageAtLevel("[Pricing] OpenAI fallback matched gpt-5.3-codex -> gpt-5.3-codex-spark", "warn"))
}

func TestGetModelPricing_Gpt54UsesStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{},
	}

	got := svc.GetModelPricing("gpt-5.4")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2.5e-7, got.CacheReadInputTokenCost, 1e-12)
	require.Equal(t, 272000, got.LongContextInputTokenThreshold)
	require.InDelta(t, 2.0, got.LongContextInputCostMultiplier, 1e-12)
	require.InDelta(t, 1.5, got.LongContextOutputCostMultiplier, 1e-12)
}

func TestGetModelPricing_OpenAICompactAliasUsesStaticFallback(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("openai/gpt5.5")
	require.NotNil(t, got)
	require.InDelta(t, 2.5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.5e-5, got.OutputCostPerToken, 1e-12)
}

func TestDefaultPricingIncludesCodexAutoReview(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	svc := &PricingService{}
	pricingData, err := svc.parsePricingData(data)
	require.NoError(t, err)
	svc.pricingData = pricingData

	got := svc.GetModelPricing("codex-auto-review")
	require.NotNil(t, got)
	require.InDelta(t, 5e-6, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 3e-5, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 5e-7, got.CacheReadInputTokenCost, 1e-12)
}

func TestGetModelPricing_Gpt54MiniUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{},
	}

	got := svc.GetModelPricing("gpt-5.4-mini")
	require.NotNil(t, got)
	require.InDelta(t, 7.5e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 4.5e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 7.5e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_Gpt54NanoUsesDedicatedStaticFallbackWhenRemoteMissing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.1-codex": {InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.GetModelPricing("gpt-5.4-nano")
	require.NotNil(t, got)
	require.InDelta(t, 2e-7, got.InputCostPerToken, 1e-12)
	require.InDelta(t, 1.25e-6, got.OutputCostPerToken, 1e-12)
	require.InDelta(t, 2e-8, got.CacheReadInputTokenCost, 1e-12)
	require.Zero(t, got.LongContextInputTokenThreshold)
}

func TestGetModelPricing_ImageModelDoesNotFallbackToTextModel(t *testing.T) {
	imagePricing := &LiteLLMModelPricing{InputCostPerToken: 3}
	textPricing := &LiteLLMModelPricing{InputCostPerToken: 9}

	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-image-2": imagePricing,
			"gpt-5.4":     textPricing,
		},
	}

	got := svc.GetModelPricing("gpt-image-3")
	require.Same(t, imagePricing, got)
}

func TestParsePricingData_PreservesPriorityAndServiceTierFields(t *testing.T) {
	raw := map[string]any{
		"gpt-5.4": map[string]any{
			"input_cost_per_token":                 2.5e-6,
			"input_cost_per_token_priority":        5e-6,
			"output_cost_per_token":                15e-6,
			"output_cost_per_token_priority":       30e-6,
			"cache_read_input_token_cost":          0.25e-6,
			"cache_read_input_token_cost_priority": 0.5e-6,
			"supports_service_tier":                true,
			"supports_prompt_caching":              true,
			"litellm_provider":                     "openai",
			"mode":                                 "chat",
		},
	}
	body, err := json.Marshal(raw)
	require.NoError(t, err)

	svc := &PricingService{}
	pricingMap, err := svc.parsePricingData(body)
	require.NoError(t, err)

	pricing := pricingMap["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 2.5e-6, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 15e-6, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 30e-6, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.25e-6, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.5e-6, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

func TestParsePricingData_PreservesServiceTierPriorityFields(t *testing.T) {
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.00003,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	pricing := pricingData["gpt-5.4"]
	require.NotNil(t, pricing)
	require.InDelta(t, 0.0000025, pricing.InputCostPerToken, 1e-12)
	require.InDelta(t, 0.000005, pricing.InputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.000015, pricing.OutputCostPerToken, 1e-12)
	require.InDelta(t, 0.00003, pricing.OutputCostPerTokenPriority, 1e-12)
	require.InDelta(t, 0.00000025, pricing.CacheReadInputTokenCost, 1e-12)
	require.InDelta(t, 0.0000005, pricing.CacheReadInputTokenCostPriority, 1e-12)
	require.True(t, pricing.SupportsServiceTier)
}

// ---------------------------------------------------------------------------
// ListModelNamesByProvider
// ---------------------------------------------------------------------------

func TestListModelNamesByProvider_ReturnsMatchingModels(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-opus-4-5-20251101": {LiteLLMProvider: "anthropic", InputCostPerToken: 1.5e-5},
			"claude-sonnet-4-5":        {LiteLLMProvider: "anthropic", InputCostPerToken: 3e-6},
			"gpt-4o":                   {LiteLLMProvider: "openai", InputCostPerToken: 5e-6},
			"gemini-2.5-pro":           {LiteLLMProvider: "google", InputCostPerToken: 1.25e-6},
		},
	}

	got := svc.ListModelNamesByProvider("anthropic")
	require.ElementsMatch(t, []string{"claude-opus-4-5-20251101", "claude-sonnet-4-5"}, got)
	// Must be sorted
	require.Equal(t, "claude-opus-4-5-20251101", got[0])
	require.Equal(t, "claude-sonnet-4-5", got[1])
}

func TestListModelNamesByProvider_CaseInsensitive(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-4o": {LiteLLMProvider: "OpenAI", InputCostPerToken: 5e-6},
		},
	}

	got := svc.ListModelNamesByProvider("openai")
	require.Equal(t, []string{"gpt-4o"}, got)

	got2 := svc.ListModelNamesByProvider("OPENAI")
	require.Equal(t, []string{"gpt-4o"}, got2)
}

func TestListModelNamesByProvider_NoMatch(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-4o": {LiteLLMProvider: "openai", InputCostPerToken: 5e-6},
		},
	}

	got := svc.ListModelNamesByProvider("anthropic")
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestListModelNamesByProvider_EmptyCatalog(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{},
	}

	got := svc.ListModelNamesByProvider("openai")
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestGetModelPricing_BareNameMatchesProviderPrefixedHighestPrice(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gemini/imagen-4.0-generate-001":    {OutputCostPerImage: 0.04, Mode: "image_generation"},
			"vertex_ai/imagen-4.0-generate-001": {OutputCostPerImage: 0.06, Mode: "image_generation"},
		},
	}
	got := svc.GetModelPricing("imagen-4.0-generate-001")
	require.NotNil(t, got)
	// 多 provider 命中取最高价（保守计费）
	require.InDelta(t, 0.06, got.OutputCostPerImage, 1e-12)
}

func TestGetModelPricing_BareNameMatchesMultiSegmentPrefix(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"aiml/google/imagen-4.0-ultra-generate-001": {OutputCostPerImage: 0.05, Mode: "image_generation"},
		},
	}
	got := svc.GetModelPricing("imagen-4.0-ultra-generate-001")
	require.NotNil(t, got)
	require.InDelta(t, 0.05, got.OutputCostPerImage, 1e-12)
}

func TestGetModelPricing_VideoBareNameMatchesPerSecondHighestPrice(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gemini/veo-3.1-generate-preview":    {OutputCostPerSecond: 0.40, Mode: "video_generation"},
			"vertex_ai/veo-3.1-generate-preview": {OutputCostPerSecond: 0.30, Mode: "video_generation"},
		},
	}
	got := svc.GetModelPricing("veo-3.1-generate-preview")
	require.NotNil(t, got)
	require.InDelta(t, 0.40, got.OutputCostPerSecond, 1e-12)
}

func TestGetModelPricing_ProviderPrefixFallbackNoFalseMatch(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gemini/imagen-4.0-generate-001": {OutputCostPerImage: 0.04},
		},
	}
	require.Nil(t, svc.GetModelPricing("nonexistent-model-xyz"))
}

func TestParsePricingData_ParsesOutputCostPerSecond(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gemini/veo-3.1-generate-preview": {
			"output_cost_per_second": 0.4,
			"litellm_provider": "gemini",
			"mode": "video_generation"
		}
	}`))
	require.NoError(t, err)
	require.NotNil(t, data["gemini/veo-3.1-generate-preview"])
	require.InDelta(t, 0.4, data["gemini/veo-3.1-generate-preview"].OutputCostPerSecond, 1e-12)
}

// TestParsePricingData_TKMediaOverlayMergesWhenSourceLacksMedia proves the
// load-bearing production path: the runtime source (Wei-Shaw mirror) carries NO
// imagen/veo entries at all — not bare, not provider-prefixed — yet imagen-*/veo-*
// must still resolve to a real price via the always-merged TK overlay. This is what
// makes the feature work in prod (matchByProviderPrefix has nothing to match there).
func TestParsePricingData_TKMediaOverlayMergesWhenSourceLacksMedia(t *testing.T) {
	svc := &PricingService{}
	// Wei-Shaw-shaped source: only mainstream token models, no media, no "/" keys.
	body := []byte(`{
		"gpt-4o": {"input_cost_per_token": 0.0000025, "output_cost_per_token": 0.00001, "litellm_provider": "openai"},
		"claude-3-7-sonnet-20250219": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "litellm_provider": "anthropic"}
	}`)
	pricingData, err := svc.parsePricingData(body)
	require.NoError(t, err)
	svc.pricingData = pricingData

	img := svc.GetModelPricing("imagen-4.0-generate-001")
	require.NotNil(t, img, "overlay must supply imagen even when the source lacks it")
	require.Equal(t, "image_generation", img.Mode)
	require.Greater(t, img.OutputCostPerImage, 0.0)

	vid := svc.GetModelPricing("veo-3.1-generate-001")
	require.NotNil(t, vid, "overlay must supply veo even when the source lacks it")
	require.Equal(t, "video_generation", vid.Mode)
	require.Greater(t, vid.OutputCostPerSecond, 0.0)
}

// TestParsePricingData_TKMediaOverlayIsFillOnly proves the overlay never overwrites
// the loaded source: the day the source carries a bare media key natively, the source
// value wins and the overlay entry is ignored (self-deprecating).
func TestParsePricingData_TKMediaOverlayIsFillOnly(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"imagen-4.0-generate-001": {"output_cost_per_image": 0.99, "mode": "image_generation", "litellm_provider": "vertex_ai"}
	}`)
	pricingData, err := svc.parsePricingData(body)
	require.NoError(t, err)
	got := pricingData["imagen-4.0-generate-001"]
	require.NotNil(t, got)
	require.InDelta(t, 0.99, got.OutputCostPerImage, 1e-12) // source wins, not the overlay's value
}
