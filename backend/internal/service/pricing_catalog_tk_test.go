//go:build unit

package service

// US-028 service-level coverage for PricingCatalogService.
// The handler tests (backend/internal/handler/us028_*) cover the HTTP contract;
// these tests cover the parser and mtime-cache behaviors that the handler
// can't see through its interface seam.

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixture uses servable claude/gpt IDs (claude-opus-4-6, gpt-5.4) so the
// public-catalog support filter (pricing_catalog_supported_models_tk.go)
// keeps them — the parser test validates parsing mechanics, not the filter.
const litellmFixtureJSON = `{
  "sample_spec": {"input_cost_per_token": 1.0},
  "claude-opus-4-6": {
    "input_cost_per_token": 0.000003,
    "output_cost_per_token": 0.000015,
    "cache_read_input_token_cost": 0.0000003,
    "cache_creation_input_token_cost": 0.00000375,
    "litellm_provider": "anthropic",
    "max_input_tokens": 200000,
    "max_output_tokens": 64000,
    "supports_vision": true,
    "supports_tool_choice": true,
    "supports_prompt_caching": true
  },
  "gpt-5.4": {
    "input_cost_per_token": 0.00000015,
    "output_cost_per_token": 0.0000006,
    "litellm_provider": "openai",
    "max_input_tokens": 128000,
    "max_output_tokens": 16384,
    "supports_function_calling": true,
    "supports_vision": true
  },
	  "broken-no-prices": {
	    "litellm_provider": "noprice"
	  },
	  "chat-image-input-only": {
	    "output_cost_per_image": 0.00012,
	    "mode": "chat",
	    "litellm_provider": "vertex_ai-language-models"
	  }
}`

func TestPricingCatalogService_ParsesLiteLLMShape(t *testing.T) {
	ts := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	// Test the pure parser directly: BuildPublicCatalog additionally fill-merges
	// the always-on TK pricing overlay (applyCatalogOverlayPricing), which would
	// add ~24 unrelated overlay models here. buildCatalogFromBytes is exactly the
	// parse-mechanics seam these assertions target.
	resp := buildCatalogFromBytes([]byte(litellmFixtureJSON), ts)
	require.NotNil(t, resp)
	assert.Equal(t, "list", resp.Object)
	assert.Equal(t, ts, resp.UpdatedAt, "updated_at must reflect source mtime")

	// sample_spec is filtered out; no-price and chat image-input-only rows are dropped.
	require.Len(t, resp.Data, 2, "expected 2 valid token-priced entries")

	// Sorted alphabetically by model_id, so claude- < gpt-.
	claude := resp.Data[0]
	assert.Equal(t, "claude-opus-4-6", claude.ModelID)
	assert.Equal(t, "anthropic", claude.Vendor)
	assert.Equal(t, "USD", claude.Pricing.Currency)
	// per-token (3e-6) * 1000 == 0.003 per 1k.
	assert.InDelta(t, 0.003, claude.Pricing.InputPer1KTokens, 1e-9)
	assert.InDelta(t, 0.015, claude.Pricing.OutputPer1KTokens, 1e-9)
	assert.InDelta(t, 0.0003, claude.Pricing.CacheReadPer1K, 1e-9)
	assert.InDelta(t, 0.00375, claude.Pricing.CacheWritePer1K, 1e-9)
	assert.Equal(t, 200000, claude.ContextWindow)
	assert.Equal(t, 64000, claude.MaxOutputTokens)
	assert.ElementsMatch(t, []string{"vision", "tool_use", "prompt_caching"}, claude.Capabilities)

	gpt := resp.Data[1]
	assert.Equal(t, "gpt-5.4", gpt.ModelID)
	assert.Equal(t, "openai", gpt.Vendor)
	// supports_function_calling alone also yields tool_use.
	assert.Contains(t, gpt.Capabilities, "tool_use")
	assert.Contains(t, gpt.Capabilities, "vision")
}

// TestPublicCatalog_SurfacesMediaUnits pins the batch-2 media surfacing: an
// image_generation entry (output_cost_per_image) and a video_generation entry
// (output_cost_per_second) — both with NO token price — must appear with their
// billing_mode + per-unit price instead of being dropped by the old token-only
// guard. This is the data the /pricing page and Studio render as "$0.04 /image"
// and "$0.40 /s" (docs/archive/studio/playground-media-redesign.md batch 2).
func TestPublicCatalog_SurfacesMediaUnits(t *testing.T) {
	const fixture = `{
	  "imagen-4.0-generate-001": {"output_cost_per_image":0.04,"mode":"image_generation","litellm_provider":"vertex_ai"},
	  "veo-3.1-generate-001":    {"output_cost_per_second":0.4,"mode":"video_generation","litellm_provider":"vertex_ai"},
	  "legacy-image-no-mode":    {"output_cost_per_image":0.02,"litellm_provider":"vertex_ai"}
	}`
	resp := buildCatalogFromBytes([]byte(fixture), time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC))
	require.NotNil(t, resp)
	byID := make(map[string]PublicCatalogModel, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ModelID] = m
	}

	img, ok := byID["imagen-4.0-generate-001"]
	require.True(t, ok, "image model must surface (not dropped by the token-only guard)")
	assert.Equal(t, "image", img.Pricing.BillingMode)
	assert.InDelta(t, 0.04, img.Pricing.OutputCostPerImage, 1e-9)
	assert.Zero(t, img.Pricing.OutputCostPerSecond)
	assert.Zero(t, img.Pricing.InputPer1KTokens, "media has no token price")

	vid, ok := byID["veo-3.1-generate-001"]
	require.True(t, ok, "video model must surface")
	assert.Equal(t, "video", vid.Pricing.BillingMode)
	assert.InDelta(t, 0.4, vid.Pricing.OutputCostPerSecond, 1e-9)
	assert.Zero(t, vid.Pricing.OutputCostPerImage)

	legacy, ok := byID["legacy-image-no-mode"]
	require.True(t, ok, "pure media rows without mode keep the backwards-compatible media fallback")
	assert.Equal(t, "image", legacy.Pricing.BillingMode)
	assert.InDelta(t, 0.02, legacy.Pricing.OutputCostPerImage, 1e-9)
}

func TestPublicCatalog_ChatRowsWithImageCostsStayTokenCatalogRows(t *testing.T) {
	const fixture = `{
	  "gemini-3.1-pro-low": {
	    "input_cost_per_token":0.000002,
	    "output_cost_per_token":0.000012,
	    "output_cost_per_image":0.00012,
	    "mode":"chat",
	    "litellm_provider":"vertex_ai-language-models"
	  }
	}`
	resp := buildCatalogFromBytes([]byte(fixture), time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))
	require.NotNil(t, resp)
	require.Len(t, resp.Data, 1)

	row := resp.Data[0]
	assert.Equal(t, "gemini-3.1-pro-low", row.ModelID)
	assert.Empty(t, row.Pricing.BillingMode, "chat rows must not become Studio media membership")
	assert.Zero(t, row.Pricing.OutputCostPerImage, "per-image fields on chat rows are not media output prices")
	assert.InDelta(t, 0.002, row.Pricing.InputPer1KTokens, 1e-12)
	assert.InDelta(t, 0.012, row.Pricing.OutputPer1KTokens, 1e-12)
}

// TestPricingCatalogService_AppliesTKOverlayPricing pins the display-side overlay
// merge: models priced ONLY in tk_pricing_overlay.json (deepseek-v4-pro, doubao-*,
// glm-5.2, plus media rows such as Veo/Grok image/video) must
// surface in the public catalog / Your-Menu with their prices, matching the
// billing path that already applies the overlay. The merge is fill-only: a model
// the file source prices natively keeps the source value (overlay never overrides).
func TestPricingCatalogService_AppliesTKOverlayPricing(t *testing.T) {
	// Healthy source: one base model + deepseek-v4-flash at a deliberately absurd
	// price so the fill-only assertion can prove the source wins over the overlay.
	const fixture = `{
	  "gpt-5.4": {"input_cost_per_token":0.0000005,"output_cost_per_token":0.000002,"litellm_provider":"openai"},
	  "deepseek-v4-flash": {"input_cost_per_token":0.999,"output_cost_per_token":0.999,"litellm_provider":"deepseek"}
	}`
	s := &PricingCatalogService{}
	s.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(fixture), time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC), true
	})

	resp := s.BuildPublicCatalog(context.Background())
	require.NotNil(t, resp)
	byID := make(map[string]PublicCatalogModel, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ModelID] = m
	}

	// overlay-only models surface with their overlay price (per-token ×1000 = per-1K).
	pro, ok := byID["deepseek-v4-pro"]
	require.True(t, ok, "overlay-only deepseek-v4-pro must surface in catalog")
	assert.InDelta(t, 0.000435*tkOfficialListBaseTaxMultiplier, pro.Pricing.InputPer1KTokens, 1e-9, "deepseek-v4-pro input = overlay official × base tax")
	assert.InDelta(t, 0.00087*tkOfficialListBaseTaxMultiplier, pro.Pricing.OutputPer1KTokens, 1e-9, "deepseek-v4-pro output = overlay official × base tax")
	_, ok = byID["doubao-seed-2-0-pro-260215"]
	assert.True(t, ok, "doubao overlay model must surface")
	glm52, ok := byID["glm-5.2"]
	require.True(t, ok, "BigModel GLM overlay model must surface")
	assert.Equal(t, PlatformNewAPI, inferPlatformFromVendor(glm52.Vendor), "zhipu provider must classify as newapi")
	cnyPer1K := func(cny float64) float64 {
		return tkCNYPerMTokToUSDPerToken(cny) * 1_000
	}
	assert.InDelta(t, cnyPer1K(8)*tkOfficialListBaseTaxMultiplier, glm52.Pricing.InputPer1KTokens, 1e-12)
	assert.InDelta(t, cnyPer1K(28)*tkOfficialListBaseTaxMultiplier, glm52.Pricing.OutputPer1KTokens, 1e-12)
	assert.InDelta(t, cnyPer1K(2)*tkOfficialListBaseTaxMultiplier, glm52.Pricing.CacheReadPer1K, 1e-12)

	veo, ok := byID["veo-3.1-generate-001"]
	require.True(t, ok, "Veo overlay media row must surface in catalog")
	assert.Equal(t, "video", veo.Pricing.BillingMode)
	assert.InDelta(t, 0.6, veo.Pricing.OutputCostPerSecond, 1e-12)
	grokImage, ok := byID["grok-imagine-image"]
	require.True(t, ok, "Grok image overlay media row must surface in catalog")
	assert.Equal(t, "image", grokImage.Pricing.BillingMode)
	assert.InDelta(t, 0.02, grokImage.Pricing.OutputCostPerImage, 1e-12)
	grokVideo, ok := byID["grok-imagine-video"]
	require.True(t, ok, "Grok video overlay media row must surface in catalog")
	assert.Equal(t, "video", grokVideo.Pricing.BillingMode)
	assert.InDelta(t, 0.08, grokVideo.Pricing.OutputCostPerSecond, 1e-12)

	filtered := FilterPublicCatalogToServable(resp)
	require.NotNil(t, filtered)
	filteredByID := make(map[string]PublicCatalogModel, len(filtered.Data))
	for _, m := range filtered.Data {
		filteredByID[m.ModelID] = m
	}
	require.Contains(t, filteredByID, "veo-3.1-generate-001", "paid-gate-proven Veo must remain in public pricing")
	require.Contains(t, filteredByID, "grok-imagine-image", "paid-gate-proven Grok image must remain in public pricing")
	require.Contains(t, filteredByID, "grok-imagine-video", "paid-gate-proven Grok video must remain in public pricing")

	// fill-only: a model present in BOTH source and overlay keeps the SOURCE price.
	flash, ok := byID["deepseek-v4-flash"]
	require.True(t, ok)
	assert.InDelta(t, 999.0*tkOfficialListBaseTaxMultiplier, flash.Pricing.InputPer1KTokens, 1e-6,
		"source price wins over overlay (fill-only); base tax applies on top of source")
}

// TestPricingCatalogService_GLMLitellmMirrorOverriddenByBigModelOverlay pins that
// stale litellm USD guesses for manifest-listed GLM models do not win over the
// BigModel-sourced overlay (prod symptom: glm-5.2 at $1.4/$4.4 per Mtok).
func TestPricingCatalogService_GLMLitellmMirrorOverriddenByBigModelOverlay(t *testing.T) {
	const fixture = `{
	  "glm-5.2": {
	    "input_cost_per_token": 1.4e-06,
	    "output_cost_per_token": 4.4e-06,
	    "cache_read_input_token_cost": 2.6e-07,
	    "litellm_provider": "zhipu",
	    "mode": "chat"
	  }
	}`
	s := &PricingCatalogService{}
	s.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(fixture), time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), true
	})

	resp := s.BuildPublicCatalog(context.Background())
	require.NotNil(t, resp)
	byID := make(map[string]PublicCatalogModel, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ModelID] = m
	}
	glm52, ok := byID["glm-5.2"]
	require.True(t, ok)
	cnyPer1K := func(cny float64) float64 {
		return tkCNYPerMTokToUSDPerToken(cny) * 1_000
	}
	assert.InDelta(t, cnyPer1K(8)*tkOfficialListBaseTaxMultiplier, glm52.Pricing.InputPer1KTokens, 1e-12)
	assert.InDelta(t, cnyPer1K(28)*tkOfficialListBaseTaxMultiplier, glm52.Pricing.OutputPer1KTokens, 1e-12)
	assert.InDelta(t, cnyPer1K(2)*tkOfficialListBaseTaxMultiplier, glm52.Pricing.CacheReadPer1K, 1e-12)
}

// TestPricingCatalogService_AttachesOverlayTiers pins that input-token interval
// (阶梯) pricing from tk_pricing_overlay.json is surfaced on Pricing.Tiers of the
// public catalog (the fix for "公开接口拍平丢掉阶梯价"), and that the flat price is
// left untouched as the first-tier base. doubao-seed-2-0-pro-260215 carries a 3-tier
// ladder in the compiled-in overlay.
func TestPricingCatalogService_AttachesOverlayTiers(t *testing.T) {
	const fixture = `{
	  "gpt-5.4": {"input_cost_per_token":0.0000005,"output_cost_per_token":0.000002,"litellm_provider":"openai"}
	}`
	s := &PricingCatalogService{}
	s.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(fixture), time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC), true
	})

	resp := s.BuildPublicCatalog(context.Background())
	require.NotNil(t, resp)
	byID := make(map[string]PublicCatalogModel, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ModelID] = m
	}

	pro, ok := byID["doubao-seed-2-0-pro-260215"]
	require.True(t, ok, "tiered overlay model must surface")
	require.Len(t, pro.Pricing.Tiers, 3, "doubao-seed-2-0-pro carries a 3-tier ladder")

	// tier 1: [0, 32000), per-token ×1000 = per-1k.
	assert.Equal(t, 0, pro.Pricing.Tiers[0].MinTokens)
	require.NotNil(t, pro.Pricing.Tiers[0].MaxTokens)
	assert.Equal(t, 32000, *pro.Pricing.Tiers[0].MaxTokens)
	assert.InDelta(t, 0.000477612*tkOfficialListBaseTaxMultiplier, pro.Pricing.Tiers[0].InputPer1KTokens, 1e-9)
	assert.InDelta(t, 0.002388060*tkOfficialListBaseTaxMultiplier, pro.Pricing.Tiers[0].OutputPer1KTokens, 1e-9)

	// top tier is open-ended (MaxTokens nil) and costs more than tier 1.
	assert.Nil(t, pro.Pricing.Tiers[2].MaxTokens, "top tier must be unbounded")
	assert.Greater(t, pro.Pricing.Tiers[2].InputPer1KTokens, pro.Pricing.Tiers[0].InputPer1KTokens)

	// flat price is left as the first-tier base (purely additive — display unchanged).
	assert.InDelta(t, pro.Pricing.Tiers[0].InputPer1KTokens, pro.Pricing.InputPer1KTokens, 1e-9,
		"flat input price must equal first tier (additive change, not a mutation)")

	// flat-priced model has no tiers.
	flat, ok := byID["gpt-5.4"]
	require.True(t, ok)
	assert.Empty(t, flat.Pricing.Tiers, "flat-priced model must not carry tiers")
}

func TestPricingCatalogService_AntigravityThinkingOverlaySurfaces(t *testing.T) {
	const fixture = `{
	  "gemini-2.5-flash": {"input_cost_per_token":0.0000003,"output_cost_per_token":0.0000025,"cache_read_input_token_cost":0.00000003,"litellm_provider":"vertex_ai-language-models"}
	}`
	s := &PricingCatalogService{}
	s.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(fixture), time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC), true
	})

	resp := FilterPublicCatalogToServable(s.BuildPublicCatalog(context.Background()))
	require.NotNil(t, resp)
	byID := make(map[string]PublicCatalogModel, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ModelID] = m
	}

	thinking, ok := byID["gemini-2.5-flash-thinking"]
	require.True(t, ok, "Antigravity thinking wire id must surface once it is priced and allowlisted")
	assert.Equal(t, "antigravity", thinking.Vendor)
	assert.InDelta(t, 0.0003, thinking.Pricing.InputPer1KTokens, 1e-12)
	assert.InDelta(t, 0.0025, thinking.Pricing.OutputPer1KTokens, 1e-12)
	assert.InDelta(t, 0.00003, thinking.Pricing.CacheReadPer1K, 1e-12)
	assert.Contains(t, thinking.Capabilities, "prompt_caching")
}

// TestPricingCatalogService_ZeroPlaceholderRowGetsOverlayPrice verifies the
// display side of the absent-or-zero fill: a source row whose every price field
// is 0.0 (litellm "cost unknown") must show the overlay price for a manifest-
// listed model, matching what billing actually charges.
func TestPricingCatalogService_ZeroPlaceholderRowGetsOverlayPrice(t *testing.T) {
	const fixture = `{
	  "gpt-5.4": {"input_cost_per_token":0.0000005,"output_cost_per_token":0.000002,"litellm_provider":"openai"},
	  "deepseek-v4-pro": {"input_cost_per_token":0.0,"output_cost_per_token":0.0,"litellm_provider":"deepseek","max_input_tokens":65536,"max_output_tokens":8192}
	}`
	s := &PricingCatalogService{}
	s.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(fixture), time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC), true
	})

	resp := s.BuildPublicCatalog(context.Background())
	require.NotNil(t, resp)
	byID := make(map[string]PublicCatalogModel, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ModelID] = m
	}

	pro, ok := byID["deepseek-v4-pro"]
	require.True(t, ok)
	assert.InDelta(t, 0.000435*tkOfficialListBaseTaxMultiplier, pro.Pricing.InputPer1KTokens, 1e-9,
		"zero placeholder row must display the overlay deepseek-v4-pro price with base tax")
	assert.InDelta(t, 0.00087*tkOfficialListBaseTaxMultiplier, pro.Pricing.OutputPer1KTokens, 1e-9)
	assert.InDelta(t, 0.000003625*tkOfficialListBaseTaxMultiplier, pro.Pricing.CacheReadPer1K, 1e-12)
	assert.Equal(t, 65536, pro.ContextWindow, "file-source row metadata must be preserved")
	assert.Equal(t, 8192, pro.MaxOutputTokens)

	filtered := FilterPublicCatalogToServable(resp)
	require.NotNil(t, filtered)
	for _, m := range filtered.Data {
		if m.ModelID == "deepseek-v4-pro" {
			return
		}
	}
	t.Fatal("manifest-listed deepseek-v4-pro must remain on the public /pricing storefront")
}

// TestPublicCatalog_FiltersUnservableClaudeAndGpt covers the support filter
// (pricing_catalog_supported_models_tk.go): retired/unservable claude + gpt
// rows are pruned, servable ones kept, newapi long-tail rows require
// display=true in tk_served_models.json, and unknown vendors stay hidden until
// a universal platform mapping exists.
func TestPublicCatalog_FiltersUnservableClaudeAndGpt(t *testing.T) {
	anthropicServable := firstMapKeyForTest(t, supportedAnthropicCatalogModels)
	openAIServable := firstMapKeyForTest(t, supportedOpenAICatalogModels)
	geminiServable := firstMapKeyForTest(t, supportedGeminiCatalogModels)
	deepSeekDisplayID := firstManifestDisplayIDForChannelTypeForTest(t, newapiconstant.ChannelTypeDeepSeek)
	fixture := fmt.Sprintf(`{
	  %q: {"input_cost_per_token":0.000005,"output_cost_per_token":0.000025,"litellm_provider":"anthropic"},
	  "claude-not-a-real-id-zzz":  {"input_cost_per_token":0.00000025,"output_cost_per_token":0.00000125,"litellm_provider":"anthropic"},
	  %q: {"input_cost_per_token":0.0000005,"output_cost_per_token":0.000002,"litellm_provider":"openai"},
	  "gpt-not-a-real-id-zzz":     {"input_cost_per_token":0.0000025,"output_cost_per_token":0.00001,"litellm_provider":"openai"},
	  %q: {"input_cost_per_token":0.00000125,"output_cost_per_token":0.00001,"litellm_provider":"vertex_ai-language-models"},
	  %q:                          {"input_cost_per_token":0.0000003,"output_cost_per_token":0.0000011,"litellm_provider":"deepseek"},
	  "deepseek-v3-2-251201":      {"input_cost_per_token":0.0000002,"output_cost_per_token":0.0000004,"litellm_provider":"volcengine"},
	  "glm-4-7-251222":            {"input_cost_per_token":0.0000001,"output_cost_per_token":0.0000001,"litellm_provider":"volcengine"},
	  "glm-4-32b-0414-128k":       {"input_cost_per_token":0.0000001,"output_cost_per_token":0.0000001,"litellm_provider":"zhipu"},
	  "glm-5-turbo":               {"input_cost_per_token":0.0000012,"output_cost_per_token":0.000004,"litellm_provider":"zhipu"},
	  "minimax-m2.7":              {"input_cost_per_token":0.000001,"output_cost_per_token":0.000008,"litellm_provider":"minimax"}
	}`, anthropicServable, openAIServable, geminiServable, deepSeekDisplayID)
	s := &PricingCatalogService{}
	s.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(fixture), time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC), true
	})
	// BuildPublicCatalog is the full priced set (also backs IsModelPriced); the
	// public /pricing presentation filter is FilterPublicCatalogToServable.
	full := s.BuildPublicCatalog(context.Background())
	require.NotNil(t, full)
	require.True(t, len(full.Data) >= 8, "BuildPublicCatalog must stay unfiltered (full priced set)")
	resp := FilterPublicCatalogToServable(full)
	require.NotNil(t, resp)

	got := make(map[string]bool, len(resp.Data))
	for _, m := range resp.Data {
		got[m.ModelID] = true
	}
	// Servable claude/gpt kept.
	assert.True(t, got[anthropicServable], "servable claude kept")
	assert.True(t, got[openAIServable], "servable gpt kept")
	// Unknown native ids are priced in the fixture but absent from the SSOT allowlists.
	assert.False(t, got["claude-not-a-real-id-zzz"], "non-allowlisted claude pruned")
	assert.False(t, got["gpt-not-a-real-id-zzz"], "non-allowlisted gpt pruned")
	assert.True(t, got[geminiServable], "gemini SSOT id passes through")
	assert.True(t, got[deepSeekDisplayID], "manifest display=true deepseek kept")
	assert.False(t, got["deepseek-v3-2-251201"], "priced-but-unlisted volcengine residue pruned")
	assert.False(t, got["glm-4-7-251222"], "withdrawn VolcEngine GLM SKU pruned from storefront (serve glm-4.7 via DashScope)")
	assert.False(t, got["glm-4-32b-0414-128k"], "withdrawn GLM SKU pruned from storefront")
	assert.False(t, got["glm-5-turbo"], "removed direct-only GLM SKU hidden from storefront")
	assert.False(t, got["minimax-m2.7"], "unmapped vendor hidden until universal mapping exists")
}

func firstMapKeyForTest(t *testing.T, m map[string]struct{}) string {
	t.Helper()
	require.NotEmpty(t, m, "SSOT map must be populated for this assertion to be meaningful")
	for k := range m {
		return k
	}
	return ""
}

func firstManifestDisplayIDForChannelTypeForTest(t *testing.T, channelType int) string {
	t.Helper()
	ids := tkServedModelsManifestDisplayPresetIDsByChannelType(channelType)
	require.NotEmpty(t, ids, "channel_type %d must have a display=true manifest sample", channelType)
	sort.Strings(ids)
	return ids[0]
}

// TestIsPublicCatalogModelSupported exercises each branch of the gate by
// reference to its backing SSOT (the supported*CatalogModels maps in
// pricing_catalog_supported_models_tk.go / the served-models manifest), not by
// duplicating their model lists here — populating or refreshing an allowlist
// there must not require a matching edit in this test.
func TestIsPublicCatalogModelSupported(t *testing.T) {
	anySSOTKey := func(t *testing.T, m map[string]struct{}) string {
		t.Helper()
		require.NotEmpty(t, m, "SSOT map must be populated for this assertion to be meaningful")
		for k := range m {
			return k
		}
		return ""
	}

	t.Run("anthropic membership follows supportedAnthropicCatalogModels", func(t *testing.T) {
		id := anySSOTKey(t, supportedAnthropicCatalogModels)
		assert.True(t, isPublicCatalogModelSupported("anthropic", id))
		assert.False(t, isPublicCatalogModelSupported("anthropic", "claude-not-a-real-id-zzz"))
	})

	t.Run("openai membership follows supportedOpenAICatalogModels", func(t *testing.T) {
		id := anySSOTKey(t, supportedOpenAICatalogModels)
		assert.True(t, isPublicCatalogModelSupported("openai", id))
		assert.False(t, isPublicCatalogModelSupported("openai", "gpt-not-a-real-id-zzz"))
	})

	t.Run("azure_openai vendor infers the openai platform gate", func(t *testing.T) {
		id := anySSOTKey(t, supportedOpenAICatalogModels)
		assert.True(t, isPublicCatalogModelSupported("azure_openai", id), "azure_openai must gate through the same openai allowlist")
	})

	t.Run("gemini membership follows supportedGeminiCatalogModels (or passes through when empty)", func(t *testing.T) {
		if len(supportedGeminiCatalogModels) == 0 {
			assert.True(t, isPublicCatalogModelSupported("vertex_ai-language-models", "anything-unprobed"), "empty (unprobed) set must passthrough")
			return
		}
		id := anySSOTKey(t, supportedGeminiCatalogModels)
		assert.True(t, isPublicCatalogModelSupported("vertex_ai-language-models", id))
		assert.False(t, isPublicCatalogModelSupported("vertex_ai-language-models", "gemini-not-a-real-id-zzz"))
	})

	t.Run("antigravity membership follows supportedAntigravityCatalogModels (or passes through when empty)", func(t *testing.T) {
		if len(supportedAntigravityCatalogModels) == 0 {
			assert.True(t, isPublicCatalogModelSupported("antigravity", "anything-unprobed"), "empty (unprobed) set must passthrough")
			return
		}
		id := anySSOTKey(t, supportedAntigravityCatalogModels)
		assert.True(t, isPublicCatalogModelSupported("antigravity", id))
		assert.False(t, isPublicCatalogModelSupported("antigravity", "claude-not-a-real-id-zzz"))
	})

	t.Run("grok membership follows supportedGrokCatalogModels (or passes through when empty)", func(t *testing.T) {
		if len(supportedGrokCatalogModels) == 0 {
			assert.True(t, isPublicCatalogModelSupported("xai", "grok-anything-unprobed"), "empty (unprobed) set must passthrough")
			return
		}
		id := anySSOTKey(t, supportedGrokCatalogModels)
		assert.True(t, isPublicCatalogModelSupported("xai", id))
		assert.False(t, isPublicCatalogModelSupported("xai", "grok-not-a-real-id-zzz"))
		// openrouter-style "x-ai" alias must resolve to the same grok gate as "xai".
		assert.Equal(t, isPublicCatalogModelSupported("xai", id), isPublicCatalogModelSupported("x-ai", id))
	})

	t.Run("dual-listed antigravity+gemini ids pass under either vendor tag", func(t *testing.T) {
		dual := ""
		for id := range supportedAntigravityCatalogModels {
			if _, ok := supportedGeminiCatalogModels[id]; ok {
				dual = id
				break
			}
		}
		if dual == "" {
			t.Skip("no dual-listed id in the current SSOT snapshot")
		}
		assert.True(t, isPublicCatalogModelSupported("antigravity", dual))
		assert.True(t, isPublicCatalogModelSupported("vertex_ai-language-models", dual))
	})

	t.Run("newapi long-tail vendor requires manifest display=true", func(t *testing.T) {
		displayed := firstManifestDisplayIDForChannelTypeForTest(t, newapiconstant.ChannelTypeDeepSeek)
		assert.True(t, isPublicCatalogModelSupported("deepseek", displayed), "owner-derived deepseek model is manifest display=true")
		assert.False(t, isPublicCatalogModelSupported("deepseek", "deepseek-totally-unlisted-zzz"))
	})

	t.Run("unmapped vendor stays hidden until a universal platform mapping exists", func(t *testing.T) {
		assert.False(t, isPublicCatalogModelSupported("minimax", "minimax-m2.7"))
		assert.False(t, isPublicCatalogModelSupported("", "anything"))
	})
}

// TestSupportedCatalogModelIDsForPlatform pins that the function returns
// exactly the keys of each platform's SSOT allowlist (no duplicates, no
// omissions) by comparing against the map itself — never by copying its model
// list into this test — so an SSOT refresh never requires editing this file.
// Regression coverage for the 2026-06-20 empty-grok-group bug (grok branch)
// and PR #1265 (antigravity branch): both platforms must actually reach their
// SSOT map, not silently fall back to the empty/unpopulated path.
func TestSupportedCatalogModelIDsForPlatform(t *testing.T) {
	cases := []struct {
		name     string
		platform string
		ssot     map[string]struct{}
	}{
		{"anthropic", PlatformAnthropic, supportedAnthropicCatalogModels},
		{"openai", PlatformOpenAI, supportedOpenAICatalogModels},
		{"antigravity", PlatformAntigravity, supportedAntigravityCatalogModels},
		{"grok", PlatformGrok, supportedGrokCatalogModels},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.NotEmpty(t, c.ssot, "SSOT map must be populated for this assertion to be meaningful")
			ids := supportedCatalogModelIDsForPlatform(c.platform)
			require.Len(t, ids, len(c.ssot), "must return exactly one entry per SSOT key (no duplicates/omissions)")
			got := make(map[string]struct{}, len(ids))
			for _, id := range ids {
				got[id] = struct{}{}
			}
			assert.Equal(t, c.ssot, got, "must mirror the platform SSOT map exactly")
		})
	}

	t.Run("unknown platform returns nil", func(t *testing.T) {
		assert.Nil(t, supportedCatalogModelIDsForPlatform("not-a-real-platform"))
	})
}

func TestPricingCatalogService_EmptyOrUnparseableSourceReturnsEmptyList(t *testing.T) {
	cases := []struct {
		name string
		src  CatalogSource
	}{
		{
			name: "ok=false (no data file)",
			src:  func() ([]byte, time.Time, bool) { return nil, time.Time{}, false },
		},
		{
			name: "ok=true but empty bytes",
			src:  func() ([]byte, time.Time, bool) { return []byte{}, time.Now(), true },
		},
		{
			name: "ok=true with garbage JSON",
			src:  func() ([]byte, time.Time, bool) { return []byte(`not-json`), time.Now(), true },
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := &PricingCatalogService{}
			s.SetSourceForTesting(tc.src)
			resp := s.BuildPublicCatalog(context.Background())
			require.NotNil(t, resp, "must never return nil — handler depends on this for AC-005 200-not-500 path")
			assert.Equal(t, "list", resp.Object)
			assert.Empty(t, resp.Data, "degraded source must yield empty data, not 500")
		})
	}
}

func TestPricingCatalogService_CachesByMTime(t *testing.T) {
	s := &PricingCatalogService{}
	ts1 := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	calls := 0
	s.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		calls++
		return []byte(litellmFixtureJSON), ts1, true
	})

	first := s.BuildPublicCatalog(context.Background())
	second := s.BuildPublicCatalog(context.Background())

	// Same mtime → second call must reuse the cached pointer (not just equal contents).
	assert.Same(t, first, second, "same mtime must hit the in-memory cache (pointer equality)")
	assert.Equal(t, 2, calls, "source closure is cheap and is still invoked to read mtime; cache decision is downstream")

	// Bumping mtime invalidates the cache and produces a fresh response.
	ts2 := ts1.Add(5 * time.Minute)
	s.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(litellmFixtureJSON), ts2, true
	})
	third := s.BuildPublicCatalog(context.Background())
	assert.NotSame(t, first, third, "advancing source mtime must invalidate cache")
	assert.Equal(t, ts2, third.UpdatedAt)
}

func TestPricingCatalogService_NilReceiverIsSafe(t *testing.T) {
	var s *PricingCatalogService
	resp := s.BuildPublicCatalog(context.Background())
	require.NotNil(t, resp, "nil receiver must still return a usable empty response")
	assert.Equal(t, "list", resp.Object)
	assert.Empty(t, resp.Data)
}

func TestFilterPublicCatalog_ReattributesAntigravityExclusiveVendor(t *testing.T) {
	// The upstream price mirror carries antigravity-served wire ids under the
	// vertex_ai vendor; without re-attribution the gemini gate (constrained Vertex
	// allowlist) drops them from the public catalog (#1029/#1030 follow-up — same
	// class as the gpt-5.6 display gap, on the antigravity surface).
	in := &PublicCatalogResponse{Object: "list", Data: []PublicCatalogModel{
		{ModelID: "gemini-3.5-flash", Vendor: "vertex_ai-language-models"},               // antigravity-exclusive, mirror-vendored
		{ModelID: "gemini-3-pro-image", Vendor: "antigravity"},                           // antigravity-exclusive, overlay-injected
		{ModelID: "gemini-2.5-flash", Vendor: "vertex_ai-language-models"},               // DUAL-listed (gemini + antigravity)
		{ModelID: "imagen-4.0-generate-001", Vendor: "vertex_ai"},                        // gemini allowlist
		{ModelID: "gemini-9-experimental-unlisted", Vendor: "vertex_ai-language-models"}, // in NO allowlist -> dropped
	}}
	out := FilterPublicCatalogToServable(in)
	require.NotNil(t, out)
	byID := map[string]PublicCatalogModel{}
	for _, m := range out.Data {
		byID[m.ModelID] = m
	}

	// antigravity-exclusive, mirror-vendored: survives + re-attributed to antigravity
	m, ok := byID["gemini-3.5-flash"]
	require.True(t, ok, "antigravity-exclusive gemini-* must survive the public filter")
	assert.Equal(t, "antigravity", m.Vendor, "re-attributed to antigravity vendor")
	// antigravity-exclusive, already overlay-injected as antigravity: survives unchanged
	m, ok = byID["gemini-3-pro-image"]
	require.True(t, ok)
	assert.Equal(t, "antigravity", m.Vendor)
	// dual-listed: survives, vendor NOT changed (genuinely Vertex-served too)
	m, ok = byID["gemini-2.5-flash"]
	require.True(t, ok, "dual-listed gemini survives")
	assert.Equal(t, "vertex_ai-language-models", m.Vendor, "dual-listed keeps gemini vendor")
	// plain gemini-allowlist model: survives, untouched
	_, ok = byID["imagen-4.0-generate-001"]
	assert.True(t, ok)
	// vertex_ai model in NO allowlist: still dropped (the gemini gate stays strict)
	_, ok = byID["gemini-9-experimental-unlisted"]
	assert.False(t, ok, "vertex_ai model in no allowlist is still dropped")

	// pure-function unit
	assert.Equal(t, "antigravity", presentationVendorForServable("gemini-3.5-flash", "vertex_ai-language-models"))
	assert.Equal(t, "vertex_ai-language-models", presentationVendorForServable("gemini-2.5-flash", "vertex_ai-language-models"))
	assert.Equal(t, "openai", presentationVendorForServable("gpt-5.6-sol", "openai")) // non-antigravity untouched
}
