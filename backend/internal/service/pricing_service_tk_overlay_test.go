package service

import (
	"encoding/json"
	"math"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestEmbeddedPricingRegistryParses(t *testing.T) {
	snapshot, err := buildTKPricingOverlaySnapshot()
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Models)
	require.NoError(t, snapshot.BaseTax.validate())

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(tkPricingOverlayRaw, &raw))
	_, leaked := snapshot.Models["_meta"]
	require.False(t, leaked, "registry provenance must not become a model owner")
}

func TestParseTKOverlayDocument_RejectsInvalidTaxPolicy(t *testing.T) {
	tests := map[string]string{
		"unknown config field":    `{"_config":{"unexpected":{}}}`,
		"out of range multiplier": `{"_config":{"official_list_base_tax":{"multiplier":0.99,"rules":[{"provider":"dashscope","model_prefixes":["qwen"]}]}}}`,
		"unknown rule field":      `{"_config":{"official_list_base_tax":{"multiplier":1.06,"rules":[{"provider":"dashscope","model_prefixes":["qwen"],"unexpected":true}]}}}`,
		"missing matcher":         `{"_config":{"official_list_base_tax":{"multiplier":1.06,"rules":[{"provider":"dashscope"}]}}}`,
	}
	for name, blob := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseTKOverlayDocument([]byte(blob))
			require.Error(t, err)
		})
	}
}

func TestParseTKOverlayDocument_RejectsMalformedOwnerRows(t *testing.T) {
	tests := map[string]string{
		"typed metadata mismatch": `{"model":{"mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"max_input_tokens":"not-an-int"}}`,
		"malformed intervals":     `{"model":{"mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"intervals":"not-an-array"}}`,
		"negative interval price": `{"model":{"mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"intervals":[{"min_tokens":0,"max_tokens":100,"input_cost_per_token":-0.000001}]}}`,
		"overlapping intervals":   `{"model":{"mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"intervals":[{"min_tokens":0,"max_tokens":100,"input_cost_per_token":0.000001},{"min_tokens":50,"max_tokens":200,"input_cost_per_token":0.000001}]}}`,
		"provider-prefixed owner": `{"provider/model":{"mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`,
		"unnormalized owner":      `{"Model ":{"mode":"chat","input_cost_per_token":0.000001,"output_cost_per_token":0.000002}}`,
	}
	for name, blob := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseTKOverlayDocument([]byte(blob))
			require.Error(t, err)
		})
	}
}

func TestProvidePricingService_FailsClosedOnInvalidRegistry(t *testing.T) {
	originalRaw := tkPricingOverlayRaw
	tkPricingOverlayRaw = []byte(`{"_config":{"unexpected":true}}`)
	tkPricingRegistryOnce = sync.Once{}
	tkPricingRegistrySnapshot = nil
	t.Cleanup(func() {
		tkPricingOverlayRaw = originalRaw
		tkPricingRegistryOnce = sync.Once{}
		tkPricingRegistrySnapshot = nil
	})

	svc, err := ProvidePricingService()
	require.Error(t, err)
	require.Nil(t, svc)
}

func TestParseTKOverlayDocument_RejectsMalformedVideoTiers(t *testing.T) {
	validTier := `{"resolution":"720p","output_cost_per_second":0.1,"default_for_model":true}`
	tests := map[string]string{
		"empty tiers":              `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[]}}`,
		"unknown resolution":       `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[{"resolution":"banana","output_cost_per_second":0.1,"default_for_model":true}]}}`,
		"noncanonical resolution":  `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[{"resolution":"720P","output_cost_per_second":0.1,"default_for_model":true}]}}`,
		"duplicate resolution":     `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[` + validTier + `,` + validTier + `]}}`,
		"missing rate":             `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[{"resolution":"720p","default_for_model":true}]}}`,
		"missing default":          `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[{"resolution":"720p","output_cost_per_second":0.1}]}}`,
		"multiple defaults":        `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"video_price_tiers":[` + validTier + `,{"resolution":"1080p","output_cost_per_second":0.2,"default_for_model":true}]}}`,
		"mismatched default field": `{"video":{"mode":"video_generation","output_cost_per_second":0.1,"default_video_resolution":"1080p","video_price_tiers":[` + validTier + `]}}`,
		"flat not minimum":         `{"video":{"mode":"video_generation","output_cost_per_second":0.2,"video_price_tiers":[` + validTier + `]}}`,
	}
	for name, blob := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseTKOverlayDocument([]byte(blob))
			require.Error(t, err)
		})
	}
}

func TestParseTKOverlayDocument_DerivesVideoDefaultFromTierOwner(t *testing.T) {
	doc, err := parseTKOverlayDocument([]byte(`{
		"video": {
			"mode": "video_generation",
			"output_cost_per_second": 0.1,
			"video_price_tiers": [
				{"resolution":"720p","output_cost_per_second":0.1},
				{"resolution":"1080p","output_cost_per_second":0.2,"default_for_model":true}
			]
		}
	}`))
	require.NoError(t, err)
	require.Equal(t, "1080p", doc.Models["video"].DefaultVideoResolution)
}

func minRegistryVideoFlatPreTax(entry *LiteLLMModelPricing) float64 {
	min := math.MaxFloat64
	for _, tier := range entry.VideoPriceTiers {
		for _, rate := range []float64{tier.PerSecond, tier.PerSecondSilent} {
			if rate > 0 && rate < min {
				min = rate
			}
		}
	}
	if min == math.MaxFloat64 {
		return 0
	}
	return min
}

func TestParsePricingData_InjectsDeepseekRegistryOwners(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"fixture-import": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002,
			"litellm_provider": "fixture",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	flash := data["deepseek-v4-flash"]
	require.Equal(t, tkOverlayLiteLLMModelPricing("deepseek-v4-flash"), flash)
	require.True(t, flash.SupportsPromptCaching)
	require.Equal(t, "deepseek", flash.LiteLLMProvider)
	require.Equal(t, "chat", flash.Mode)

	pro := data["deepseek-v4-pro"]
	require.Equal(t, tkOverlayLiteLLMModelPricing("deepseek-v4-pro"), pro)
}

func TestParsePricingData_InjectsManifestMoonshotRegistryOwners(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"fixture-import": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "litellm_provider": "fixture", "mode": "chat"}
	}`))
	require.NoError(t, err)

	for _, modelID := range tkServedModelsManifestPresetIDsByChannelType(25) {
		pricing := data[modelID]
		require.Equal(t, tkOverlayLiteLLMModelPricing(modelID), pricing,
			"parsed data must use the manifest model's registry owner")
		require.False(t, tkIsEffectivelyUnpriced(pricing), "Moonshot model %s must never resolve to a zero price", modelID)
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
	require.InDelta(t, data["kimi-k3"].InputCostPerToken*tkOfficialListBaseTaxMultiplier(), k3.InputPricePerToken, 1e-15)
	require.InDelta(t, data["kimi-k3"].OutputCostPerToken*tkOfficialListBaseTaxMultiplier(), k3.OutputPricePerToken, 1e-15)
	require.InDelta(t, data["kimi-k3"].CacheReadInputTokenCost*tkOfficialListBaseTaxMultiplier(), k3.CacheReadPricePerToken, 1e-15)
}

func TestBillingWithoutPricingServiceUsesRegistryOwner(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	pricing, err := billing.GetModelPricing("kimi-k2.6")
	require.NoError(t, err)
	want := tkApplyOfficialListBaseTaxForModel("kimi-k2.6", tkOverlayModelPricing("kimi-k2.6"))
	require.NotNil(t, want)
	require.InDelta(t, want.InputPricePerToken, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, want.OutputPricePerToken, pricing.OutputPricePerToken, 1e-15)
	require.InDelta(t, want.CacheReadPricePerToken, pricing.CacheReadPricePerToken, 1e-15)
}

func TestBillingRegistryMediaOnlyPricingRemainsTokenAbsent(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"fixture-import": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "litellm_provider": "fixture", "mode": "chat"}
	}`))
	require.NoError(t, err)
	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	pricing, err := billing.GetModelPricing("grok-imagine-image")
	require.ErrorIs(t, err, ErrModelPricingUnavailable)
	require.Nil(t, pricing)
}

func TestTKPricingRegistryOwnerWinsOverImportedEvidence(t *testing.T) {
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
	owner := tkOverlayLiteLLMModelPricing("deepseek-v4-flash")
	require.NotNil(t, owner)
	require.Equal(t, owner, flash, "registry owner must replace conflicting imported evidence")
}

func TestTKPricingRegistryGPT56OwnsPriceAcrossServices(t *testing.T) {
	// A conflicting imported source row must never become a second runtime owner.
	// The unified registry row is the only base price, and both service entry
	// points resolve it without a Go numeric fallback.
	pricingService := &PricingService{}
	data, err := pricingService.parsePricingData([]byte(`{
		"gpt-5.6-terra": {
			"input_cost_per_token": 0.000099,
			"output_cost_per_token": 0.000999,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)
	gotSource := data["gpt-5.6-terra"]
	require.NotNil(t, gotSource)
	owner := tkOverlayLiteLLMModelPricing("gpt-5.6-terra")
	require.NotNil(t, owner)
	require.Equal(t, owner, gotSource)

	pricingService.pricingData = map[string]*LiteLLMModelPricing{}
	fromPricingService := pricingService.GetModelPricing("gpt-5.6-terra-preview")
	require.NotNil(t, fromPricingService)
	require.InDelta(t, owner.InputCostPerTokenPriority, fromPricingService.InputCostPerTokenPriority, 1e-15)
	require.InDelta(t, owner.OutputCostPerTokenPriority, fromPricingService.OutputCostPerTokenPriority, 1e-15)
	require.InDelta(t, owner.CacheCreationInputTokenCost, fromPricingService.CacheCreationInputTokenCost, 1e-15)
	require.Equal(t, owner.LongContextInputTokenThreshold, fromPricingService.LongContextInputTokenThreshold)
	require.True(t, fromPricingService.SupportsPromptCaching)
	require.True(t, fromPricingService.SupportsServiceTier)
	fromBillingRegistry, err := NewBillingService(&config.Config{}, nil).GetModelPricing("gpt-5.6-terra")
	require.NoError(t, err)
	require.NotNil(t, fromBillingRegistry)
	require.InDelta(t, fromPricingService.InputCostPerToken, fromBillingRegistry.InputPricePerToken, 1e-15)
	require.InDelta(t, fromPricingService.OutputCostPerToken, fromBillingRegistry.OutputPricePerToken, 1e-15)
	require.InDelta(t, owner.InputCostPerTokenPriority, fromBillingRegistry.InputPricePerTokenPriority, 1e-15)
	require.Equal(t, owner.LongContextInputTokenThreshold, fromBillingRegistry.LongContextInputThreshold)
}

func TestParsePricingData_UsesAntigravityGeminiThinkingOwner(t *testing.T) {
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
	require.Equal(t, tkOverlayLiteLLMModelPricing("gemini-2.5-flash-thinking"), thinking)
	require.True(t, thinking.SupportsPromptCaching)
}

func TestParsePricingData_UsesGeminiProAgentOwner(t *testing.T) {
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
	require.Equal(t, tkOverlayLiteLLMModelPricing("gemini-pro-agent"), proAgent)
}

func TestParsePricingData_UsesGemini35LiteAnd36FlashOwners(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"gemini-2.5-flash": {
			"input_cost_per_token": 0.0000003,
			"output_cost_per_token": 0.0000025,
			"litellm_provider": "vertex_ai-language-models",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)

	for _, modelID := range []string{"gemini-3.5-flash-lite", "gemini-3.6-flash"} {
		pricing := data[modelID]
		require.Equal(t, tkOverlayLiteLLMModelPricing(modelID), pricing)
		require.True(t, pricing.SupportsPromptCaching, modelID)
	}
}

func TestTKPricingRegistry_ZeroImportedPlaceholderIsReplaced(t *testing.T) {
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
	require.Equal(t, tkOverlayLiteLLMModelPricing("deepseek-v4-pro"), pro,
		"zero imported placeholder must be replaced by the registry owner")
}

func TestApplyTKPricingRegistry_GLMOwnerWinsOverImportedEvidence(t *testing.T) {
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
	require.Equal(t, tkOverlayLiteLLMModelPricing("glm-5.2"), glm)
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

func TestTKPricingRegistry_MediaEntriesStillPresent(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"fixture-import": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "litellm_provider": "fixture", "mode": "chat"}
	}`))
	require.NoError(t, err)

	imagen := data["imagen-4.0-generate-001"]
	require.Equal(t, tkOverlayLiteLLMModelPricing("imagen-4.0-generate-001"), imagen)

	veo := data["veo-3.0-generate-001"]
	require.Equal(t, tkOverlayLiteLLMModelPricing("veo-3.0-generate-001"), veo)
	require.NotEmpty(t, veo.VideoPriceTiers)
	require.InDelta(t, minRegistryVideoFlatPreTax(veo), veo.OutputCostPerSecond, 1e-12)
}

func TestTKPricingRegistry_SeedMediaEntries(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"fixture-import": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "litellm_provider": "fixture", "mode": "chat"}
	}`))
	require.NoError(t, err)
	for _, model := range []string{
		"doubao-seedream-4-0-250828",
		"doubao-seedream-4-5-251128",
		"doubao-seedream-5-0-260128",
		"seedream-4-0-250828",
	} {
		entry := data[model]
		require.Equal(t, tkOverlayLiteLLMModelPricing(model), entry)
		require.Positive(t, entry.OutputCostPerImage)
	}
	for _, model := range []string{
		"doubao-seedance-1-0-pro-250528",
		"seedance-1-0-pro-250528",
		"doubao-seedance-1-0-pro-fast-251015",
		"doubao-seedance-1-5-pro-251215",
		"doubao-seedance-2-0-260128",
		"doubao-seedance-2-0-fast-260128",
	} {
		entry := data[model]
		require.Equal(t, tkOverlayLiteLLMModelPricing(model), entry)
		require.NotEmpty(t, entry.VideoPriceTiers, model)
		require.InDelta(t, minRegistryVideoFlatPreTax(entry), entry.OutputCostPerSecond, 1e-12, model)
	}
}

func TestTKPricingRegistry_CopiesCacheCreation1hPrice(t *testing.T) {
	fable := tkOverlayLiteLLMModelPricing("claude-fable-5")
	require.NotNil(t, fable, "registry must carry claude-fable-5")
	require.Positive(t, fable.CacheCreationInputTokenCost)
	require.Greater(t, fable.CacheCreationInputTokenCostAbove1hr, fable.CacheCreationInputTokenCost,
		"registry loader must preserve the distinct 1h cache-write tier")
}

func TestBilling_FableRegistryOwnerEnablesCacheBreakdown(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"fixture-import": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "litellm_provider": "fixture", "mode": "chat"}
	}`))
	require.NoError(t, err)
	owner := tkOverlayLiteLLMModelPricing("claude-fable-5")
	require.Equal(t, owner, data["claude-fable-5"])

	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	pricing, err := billing.GetModelPricing("claude-fable-5")
	require.NoError(t, err)
	require.True(t, pricing.SupportsCacheBreakdown, "1h > 5m price must enable breakdown")
	require.InDelta(t, owner.CacheCreationInputTokenCost, pricing.CacheCreation5mPrice, 1e-15)
	require.InDelta(t, owner.CacheCreationInputTokenCostAbove1hr, pricing.CacheCreation1hPrice, 1e-15)
}

// TestBilling_Fable1hCacheCreationCost_ProdShape is the regression reproduction
// with the exact prod token shape that exposed the bug (usage_logs, 2026-06):
// cache_creation_5m_tokens=0, cache_creation_1h_tokens=684124. Correct cost is
// Before the fix the loader dropped the 1h field and billed this shape at the
// 5m tier. Expected cost is derived from the registry owner below.
func TestBilling_Fable1hCacheCreationCost_ProdShape(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"fixture-import": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "litellm_provider": "fixture", "mode": "chat"}
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
	owner := tkOverlayLiteLLMModelPricing("claude-fable-5")
	require.NotNil(t, owner)
	want := float64(tokens.CacheCreation1hTokens) * owner.CacheCreationInputTokenCostAbove1hr
	require.InDelta(t, want, breakdown.CacheCreationCost, 1e-6)
}

// The flexible parser remains available for offline imports. This fixture
// verifies that it preserves a distinct 1h cache tier without naming a product.
func TestBilling_OfflineImportCarries1hPrice(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"fixture-offline-cache-tier": {
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
	pricing, err := billing.GetModelPricing("fixture-offline-cache-tier")
	require.NoError(t, err)
	require.True(t, pricing.SupportsCacheBreakdown)
	require.InDelta(t, 6.25e-6, pricing.CacheCreation5mPrice, 1e-15)
	require.InDelta(t, 1e-5, pricing.CacheCreation1hPrice, 1e-15)

	breakdown, err := billing.CalculateCost("fixture-offline-cache-tier", UsageTokens{
		CacheCreationTokens:   1000000,
		CacheCreation5mTokens: 400000,
		CacheCreation1hTokens: 600000,
	}, 1.0)
	require.NoError(t, err)
	// 400000*6.25e-6 + 600000*1e-5 = 2.5 + 6.0
	require.InDelta(t, 8.5, breakdown.CacheCreationCost, 1e-9)
}

func TestTKPricingRegistry_RetiredQwen25CoderOwnersKeepTierStructure(t *testing.T) {
	svc := &PricingService{}
	body := []byte(`{
		"fixture-import": {
			"input_cost_per_token": 0.000001,
			"output_cost_per_token": 0.000002,
			"litellm_provider": "fixture",
			"mode": "chat"
		}
	}`)

	data, err := svc.parsePricingData(body)
	require.NoError(t, err)

	c32 := data["qwen2.5-coder-32b"]
	require.Equal(t, tkOverlayLiteLLMModelPricing("qwen2.5-coder-32b"), c32)
	require.Len(t, c32.Intervals, 4, "32b must carry the 4 input-token tiers")
	top32 := c32.Intervals[len(c32.Intervals)-1]
	require.Nil(t, top32.MaxTokens, "last 32b tier is unbounded (>256K)")
	require.NotNil(t, top32.InputPrice)
	require.NotNil(t, top32.OutputPrice)
	require.Positive(t, *top32.InputPrice)
	require.Positive(t, *top32.OutputPrice)

	c7 := data["qwen2.5-coder-7b"]
	require.Equal(t, tkOverlayLiteLLMModelPricing("qwen2.5-coder-7b"), c7)
	require.Len(t, c7.Intervals, 4, "7b must carry the 4 input-token tiers")
	base7 := c7.Intervals[0]
	require.NotNil(t, base7.InputPrice)
	require.Positive(t, *base7.InputPrice)
}
