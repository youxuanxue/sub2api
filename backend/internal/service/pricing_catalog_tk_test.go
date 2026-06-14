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

	// sample_spec is filtered out; broken-no-prices is dropped (no price fields).
	require.Len(t, resp.Data, 2, "expected 2 valid entries (sample_spec + no-price entry skipped)")

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

// TestPricingCatalogService_AppliesTKOverlayPricing pins the display-side overlay
// merge: models priced ONLY in tk_pricing_overlay.json (deepseek-v4-pro, doubao-*,
// glm-4-7-251222 — the VolcEngine fifth-platform batch + deepseek) must surface in
// the public catalog / Your-Menu with their prices, matching the billing path that
// already applies the overlay. The merge is fill-only: a model the file source
// prices natively keeps the source value (overlay never overrides).
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

	// fill-only: a model present in BOTH source and overlay keeps the SOURCE price.
	flash, ok := byID["deepseek-v4-flash"]
	require.True(t, ok)
	assert.InDelta(t, 999.0, flash.Pricing.InputPer1KTokens, 1e-6,
		"source price wins over overlay (fill-only); overlay must NOT override")
}

// TestPricingCatalogService_ZeroPlaceholderRowGetsOverlayPrice verifies the
// display side of the absent-or-zero fill: a source row whose every price field
// is 0.0 (litellm "cost unknown" — the prod shape of deepseek-v3-2-251201 under
// volcengine) must show the overlay price in the public catalog, matching what
// billing actually charges. Row metadata (context window) stays from the file
// source.
func TestPricingCatalogService_ZeroPlaceholderRowGetsOverlayPrice(t *testing.T) {
	const fixture = `{
	  "gpt-5.4": {"input_cost_per_token":0.0000005,"output_cost_per_token":0.000002,"litellm_provider":"openai"},
	  "deepseek-v3-2-251201": {"input_cost_per_token":0.0,"output_cost_per_token":0.0,"litellm_provider":"volcengine","max_input_tokens":98304,"max_output_tokens":32768}
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

	v32, ok := byID["deepseek-v3-2-251201"]
	require.True(t, ok)
	assert.InDelta(t, 2.0/6.7e3, v32.Pricing.InputPer1KTokens, 1e-12,
		"zero placeholder row must display the overlay Ark price (¥2/M ÷ 6.7 × 1K)")
	assert.InDelta(t, 3.0/6.7e3, v32.Pricing.OutputPer1KTokens, 1e-12)
	assert.InDelta(t, 0.4/6.7e3, v32.Pricing.CacheReadPer1K, 1e-12)
	assert.Equal(t, 98304, v32.ContextWindow, "file-source row metadata must be preserved")
	assert.Equal(t, 32768, v32.MaxOutputTokens)
}

// TestPublicCatalog_FiltersUnservableClaudeAndGpt covers the support filter
// (pricing_catalog_supported_models_tk.go): retired/unservable claude + gpt
// rows are pruned, servable ones kept, and every non-claude/gpt vendor passes
// through untouched.
func TestPublicCatalog_FiltersUnservableClaudeAndGpt(t *testing.T) {
	const fixture = `{
	  "claude-opus-4-8":           {"input_cost_per_token":0.000005,"output_cost_per_token":0.000025,"litellm_provider":"anthropic"},
	  "claude-3-haiku-20240307":   {"input_cost_per_token":0.00000025,"output_cost_per_token":0.00000125,"litellm_provider":"anthropic"},
	  "claude-opus-4-6-20260205":  {"input_cost_per_token":0.000003,"output_cost_per_token":0.000015,"litellm_provider":"anthropic"},
	  "gpt-5.4":                   {"input_cost_per_token":0.0000005,"output_cost_per_token":0.000002,"litellm_provider":"openai"},
	  "gpt-4o":                    {"input_cost_per_token":0.0000025,"output_cost_per_token":0.00001,"litellm_provider":"openai"},
	  "gpt-3.5-turbo":             {"input_cost_per_token":0.0000005,"output_cost_per_token":0.0000015,"litellm_provider":"openai"},
	  "gemini-2.5-pro":            {"input_cost_per_token":0.00000125,"output_cost_per_token":0.00001,"litellm_provider":"vertex_ai-language-models"},
	  "deepseek-chat":             {"input_cost_per_token":0.0000003,"output_cost_per_token":0.0000011,"litellm_provider":"deepseek"}
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
	// Other vendors untouched (filter only curates claude + gpt families).
	assert.True(t, got["gemini-2.5-pro"], "gemini vendor passes through")
	assert.True(t, got["deepseek-chat"], "non-curated vendor passes through")
}

func TestIsPublicCatalogModelSupported(t *testing.T) {
	cases := []struct {
		vendor, model string
		want          bool
	}{
		{"anthropic", "claude-opus-4-8", true},
		{"anthropic", "claude-3-haiku-20240307", false},
		{"anthropic", "claude-opus-4-6-20260205", false},
		{"openai", "gpt-5.4", true},
		{"openai", "gpt-5-mini", true},   // servable extra beyond canonical
		{"openai", "gpt-5.2", false},     // canonical but probe-unservable (502)
		{"openai", "gpt-image-2", false}, // not servable on a probeable path
		{"openai", "gpt-4o", false},
		{"azure_openai", "gpt-4", false},                      // azure_openai → openai platform, gated
		{"vertex_ai-language-models", "gemini-2.5-pro", true}, // other vendor: pass-through
		{"deepseek", "deepseek-chat", true},
		// antigravity (2026-06-13 empirical probe): gated to the gemini-only set.
		{"antigravity", "gemini-3.5-flash-low", true},
		{"antigravity", "gpt-oss-120b-medium", false}, // gpt-oss off antigravity (operator policy)
		{"antigravity", "claude-sonnet-4-6", false},   // claude routed to anthropic
		{"antigravity", "gemini-2.5-pro", false},      // 503 at probe, not in antigravity set
		{"", "anything", true},                        // unknown vendor: pass-through
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, isPublicCatalogModelSupported(c.vendor, c.model),
			"vendor=%q model=%q", c.vendor, c.model)
	}
}

// 直接固化 supportedCatalogModelIDsForPlatform 的 antigravity 契约：实测 gemini-only
// 集合（claude 走 anthropic、gpt-oss 已从 antigravity 移除）。注意此 accessor 目前
// 仅由本测试触达——线上 Your-Menu fallback 对 antigravity 走 DefaultAntigravityModelMapping
// 而非此函数；保留 antigravity 分支是为与公共目录 gate 对称，未来接线即正确。
func TestSupportedCatalogModelIDsForPlatform_Antigravity(t *testing.T) {
	ids := supportedCatalogModelIDsForPlatform(PlatformAntigravity)
	require.NotEmpty(t, ids)
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	for _, want := range []string{"gemini-3.5-flash-low", "gemini-pro-agent"} {
		_, ok := set[want]
		assert.Truef(t, ok, "expected antigravity menu to advertise %q", want)
	}
	for _, deny := range []string{"claude-sonnet-4-6", "gpt-oss-120b-medium"} {
		_, ok := set[deny]
		assert.Falsef(t, ok, "antigravity menu must not advertise %q (routed off antigravity)", deny)
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
