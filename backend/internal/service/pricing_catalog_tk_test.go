//go:build unit

package service

// US-028 service-level coverage for PricingCatalogService.
// The handler tests (backend/internal/handler/us028_*) cover the HTTP contract;
// these tests cover the parser and mtime-cache behaviors that the handler
// can't see through its interface seam.

import (
	"context"
	"testing"
	"time"

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
// and "$0.40 /s" (docs/playground-media-redesign.md batch 2).
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
// glm-4-7-251222 / glm-5.2, plus media rows such as Veo/Grok image/video) must
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
	assert.InDelta(t, 0.000435, pro.Pricing.InputPer1KTokens, 1e-9, "deepseek-v4-pro input = overlay $0.435/M")
	assert.InDelta(t, 0.00087, pro.Pricing.OutputPer1KTokens, 1e-9, "deepseek-v4-pro output = overlay $0.87/M")
	_, ok = byID["doubao-seed-2-0-pro-260215"]
	assert.True(t, ok, "doubao overlay model must surface")
	_, ok = byID["glm-4-7-251222"]
	assert.True(t, ok, "glm-4-7 overlay model must surface")
	glm52, ok := byID["glm-5.2"]
	require.True(t, ok, "direct Z.AI GLM overlay model must surface")
	assert.Equal(t, PlatformNewAPI, inferPlatformFromVendor(glm52.Vendor), "zhipu provider must classify as newapi")
	assert.InDelta(t, 0.0014, glm52.Pricing.InputPer1KTokens, 1e-12)
	assert.InDelta(t, 0.0044, glm52.Pricing.OutputPer1KTokens, 1e-12)
	assert.InDelta(t, 0.00026, glm52.Pricing.CacheReadPer1K, 1e-12)

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
	assert.InDelta(t, 999.0, flash.Pricing.InputPer1KTokens, 1e-6,
		"source price wins over overlay (fill-only); overlay must NOT override")
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
	assert.InDelta(t, 0.000477612, pro.Pricing.Tiers[0].InputPer1KTokens, 1e-9)
	assert.InDelta(t, 0.002388060, pro.Pricing.Tiers[0].OutputPer1KTokens, 1e-9)

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
	assert.InDelta(t, 0.000435, pro.Pricing.InputPer1KTokens, 1e-9,
		"zero placeholder row must display the overlay deepseek-v4-pro price")
	assert.InDelta(t, 0.00087, pro.Pricing.OutputPer1KTokens, 1e-9)
	assert.InDelta(t, 0.000003625, pro.Pricing.CacheReadPer1K, 1e-12)
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
	const fixture = `{
	  "claude-opus-4-8":           {"input_cost_per_token":0.000005,"output_cost_per_token":0.000025,"litellm_provider":"anthropic"},
	  "claude-3-haiku-20240307":   {"input_cost_per_token":0.00000025,"output_cost_per_token":0.00000125,"litellm_provider":"anthropic"},
	  "claude-opus-4-6-20260205":  {"input_cost_per_token":0.000003,"output_cost_per_token":0.000015,"litellm_provider":"anthropic"},
	  "gpt-5.4":                   {"input_cost_per_token":0.0000005,"output_cost_per_token":0.000002,"litellm_provider":"openai"},
	  "gpt-4o":                    {"input_cost_per_token":0.0000025,"output_cost_per_token":0.00001,"litellm_provider":"openai"},
	  "gpt-3.5-turbo":             {"input_cost_per_token":0.0000005,"output_cost_per_token":0.0000015,"litellm_provider":"openai"},
	  "gemini-2.5-pro":            {"input_cost_per_token":0.00000125,"output_cost_per_token":0.00001,"litellm_provider":"vertex_ai-language-models"},
	  "deepseek-chat":             {"input_cost_per_token":0.0000003,"output_cost_per_token":0.0000011,"litellm_provider":"deepseek"},
	  "deepseek-v3-2-251201":      {"input_cost_per_token":0.0000002,"output_cost_per_token":0.0000004,"litellm_provider":"volcengine"},
	  "glm-4-32b-0414-128k":       {"input_cost_per_token":0.0000001,"output_cost_per_token":0.0000001,"litellm_provider":"zhipu"},
	  "glm-5-turbo":               {"input_cost_per_token":0.0000012,"output_cost_per_token":0.000004,"litellm_provider":"zhipu"},
	  "minimax-m2.7":              {"input_cost_per_token":0.000001,"output_cost_per_token":0.000008,"litellm_provider":"minimax"}
	}`
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
	assert.True(t, got["claude-opus-4-8"], "servable claude kept")
	assert.True(t, got["gpt-5.4"], "servable gpt kept")
	// Retired / dated-dup / unservable claude+gpt pruned.
	assert.False(t, got["claude-3-haiku-20240307"], "retired claude pruned")
	assert.False(t, got["claude-opus-4-6-20260205"], "dated-snapshot claude pruned")
	assert.False(t, got["gpt-4o"], "unservable gpt pruned")
	assert.False(t, got["gpt-3.5-turbo"], "legacy gpt pruned")
	assert.True(t, got["gemini-2.5-pro"], "gemini vendor passes through")
	assert.True(t, got["deepseek-chat"], "manifest display=true deepseek kept")
	assert.False(t, got["deepseek-v3-2-251201"], "priced-but-unlisted volcengine residue pruned")
	assert.False(t, got["glm-4-32b-0414-128k"], "withdrawn GLM SKU pruned from storefront")
	assert.False(t, got["glm-5-turbo"], "removed direct-only GLM SKU hidden from storefront")
	assert.False(t, got["minimax-m2.7"], "unmapped vendor hidden until universal mapping exists")
}

func TestIsPublicCatalogModelSupported(t *testing.T) {
	cases := []struct {
		vendor, model string
		want          bool
	}{
		{"anthropic", "claude-opus-4-8", true},
		{"anthropic", "claude-fable-5", true},
		{"anthropic", "claude-opus-4-1", true},
		{"anthropic", "claude-3-haiku-20240307", false},
		{"anthropic", "claude-opus-4-6-20260205", false},
		{"openai", "gpt-5.4", true},
		{"openai", "gpt-5.5-pro", false},
		{"openai", "gpt-5.6-sol", false},
		{"openai", "gpt-5.6-terra", false},
		{"openai", "gpt-5.6-luna", false},
		{"openai", "gpt-5.6", false},
		{"openai", "gpt-5.6-chat-latest", false},
		{"openai", "gpt-5-mini", true},   // servable extra beyond canonical
		{"openai", "gpt-5.2", false},     // canonical but probe-unservable (502)
		{"openai", "gpt-image-2", false}, // not servable on a probeable path
		{"openai", "gpt-4o", false},
		{"azure_openai", "gpt-4", false},                      // azure_openai → openai platform, gated
		{"vertex_ai-language-models", "gemini-2.5-pro", true}, // other vendor: pass-through
		{"deepseek", "deepseek-chat", true},
		{"volcengine", "deepseek-v3-2-251201", false},
		{"zhipu", "glm-4-32b-0414-128k", false},
		{"zhipu", "glm-5.2", true},
		{"zhipu", "glm-5-turbo", false}, // direct-only GLM pool removed; no manifest display path
		// antigravity (2026-06-13 empirical probe, refreshed 2026-06-23): gated to the gemini-only set.
		{"antigravity", "gemini-2.5-flash", true},
		{"antigravity", "gemini-2.5-flash-lite", true},
		{"antigravity", "gemini-2.5-flash-thinking", true},
		{"antigravity", "gemini-3-flash", true},
		{"antigravity", "gemini-3.5-flash", true}, // 2026-06-27 prod 200 → added to antigravity
		{"antigravity", "gemini-3.5-flash-low", true},
		{"antigravity", "gpt-oss-120b-medium", false}, // gpt-oss off antigravity (operator policy)
		{"antigravity", "claude-sonnet-4-6", false},   // claude routed to anthropic
		{"antigravity", "gemini-2.5-pro", false},      // 000 timeout at 2026-06-23/06-27 reprobe, not in antigravity set
		// gemini-*-image probed servable through the ANTIGRAVITY pool (2026-06-27) →
		// listed under antigravity (group-serving rule), NOT the gemini/Vertex set
		// (Vertex's constrained 7-key mapping does not serve them).
		{"antigravity", "gemini-2.5-flash-image", true},
		{"antigravity", "gemini-3-pro-image", true},
		{"antigravity", "gemini-3.1-flash-image", true},
		{"antigravity", "gemini-3.1-flash-image-preview", true},
		{"vertex_ai-language-models", "gemini-3.1-flash-image", false}, // not served by gemini/Vertex accounts
		{"vertex_ai", "veo-3.1-generate-001", true},                    // 2026-07-04 post-#1198 paid gate: keep_displayed
		// grok (xai vendor → grok platform): gated to the paid-gate-verified native set.
		{"xai", "grok-4.3", true},
		{"xai", "grok-4.20-0309-reasoning", true},
		{"xai", "grok-build-0.1", true},
		{"xai", "grok-code-fast-1", true},
		{"xai", "grok-imagine-video", true},  // 2026-07-04 post-#1198 paid gate: keep_displayed
		{"x-ai", "grok-imagine-image", true}, // openrouter-style x-ai alias maps too
		{"xai", "grok-imagine-image-quality", true},
		{"xai", "grok-4", false},      // third-party-priced / unverified legacy slug
		{"xai", "grok-latest", false}, // priced alias, not public-listed
		{"volcengine", "doubao-seedream-4-0-250828", true},
		{"volcengine", "doubao-seedance-1-0-pro-250528", true},
		{"", "anything", false},             // unknown vendor: hidden
		{"minimax", "minimax-m2.7", false},  // priced but not mapped to universal platform
		{"moonshot", "kimi-k2.6", false},    // priced but not mapped to universal platform
		{"bedrock", "bedrock-model", false}, // priced but not mapped to universal platform
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, isPublicCatalogModelSupported(c.vendor, c.model),
			"vendor=%q model=%q", c.vendor, c.model)
	}
}

// 直接固化 supportedCatalogModelIDsForPlatform 的 antigravity 契约：实测 gemini-only
// 集合（claude 走 anthropic、gpt-oss 已从 antigravity 移除）。gateway
// /antigravity/models 和 admin selector 都经 tkServableCandidateIDs 消费这份集合。
func TestSupportedCatalogModelIDsForPlatform_Antigravity(t *testing.T) {
	ids := supportedCatalogModelIDsForPlatform(PlatformAntigravity)
	require.NotEmpty(t, ids)
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	for _, want := range []string{
		"gemini-2.5-flash",
		"gemini-2.5-flash-image", // 2026-06-27 antigravity image probe 200 → added
		"gemini-2.5-flash-lite",
		"gemini-2.5-flash-thinking",
		"gemini-3-flash",
		"gemini-3-flash-agent",
		"gemini-3-pro-image",             // 2026-06-27 antigravity image probe 200 → added
		"gemini-3.1-flash-image",         // served via antigravity pool
		"gemini-3.1-flash-image-preview", // 2026-06-27 antigravity image probe 200 → added
		"gemini-3.1-pro-low",
		"gemini-3.5-flash", // 2026-06-27 prod 200 → added
		"gemini-3.5-flash-extra-low",
		"gemini-3.5-flash-low",
		"gemini-pro-agent",
	} {
		_, ok := set[want]
		assert.Truef(t, ok, "expected antigravity menu to advertise %q", want)
	}
	// gemini-2.5-pro stays off antigravity (no real 200 — probe timeout 06-23 & 06-27);
	// it is served via the gemini/Vertex pool instead.
	for _, deny := range []string{"claude-sonnet-4-6", "gpt-oss-120b-medium", "gemini-2.5-pro"} {
		_, ok := set[deny]
		assert.Falsef(t, ok, "antigravity menu must not advertise %q (routed off antigravity)", deny)
	}
}

// TestSupportedCatalogModelIDsForPlatform_Grok pins the grok served set used by
// the per-user menu fallback (platformDefaultModelIDs) AND the admin
// model-whitelist selector (tkServableCandidateIDs): priced grok overlay models
// whose native-grok live probe returned 200. Regression for the 2026-06-20
// empty-grok-group bug.
func TestSupportedCatalogModelIDsForPlatform_Grok(t *testing.T) {
	ids := supportedCatalogModelIDsForPlatform(PlatformGrok)
	require.NotEmpty(t, ids)
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	for _, want := range []string{
		"grok-4.3",
		"grok-4.20-0309-reasoning",
		"grok-4.20-0309-non-reasoning",
		"grok-build-0.1",
		"grok-code-fast-1",
		"grok-imagine-image",
		"grok-imagine-image-quality",
		"grok-imagine-video",
	} {
		_, ok := set[want]
		assert.Truef(t, ok, "expected grok menu to advertise %q", want)
	}
	for _, deny := range []string{
		"grok-4",
		"grok-latest",
		"grok-code-fast-1-0825",
		"claude-opus-4-8",
	} {
		_, ok := set[deny]
		assert.Falsef(t, ok, "grok menu must not advertise %q (unpriced/off-platform)", deny)
	}
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
