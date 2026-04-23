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

const litellmFixtureJSON = `{
  "sample_spec": {"input_cost_per_token": 1.0},
  "claude-sonnet-4.5": {
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
  "gpt-4o-mini": {
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
	s := &PricingCatalogService{}
	ts := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	s.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(litellmFixtureJSON), ts, true
	})

	resp := s.BuildPublicCatalog(context.Background())
	require.NotNil(t, resp)
	assert.Equal(t, "list", resp.Object)
	assert.Equal(t, ts, resp.UpdatedAt, "updated_at must reflect source mtime")

	// sample_spec is filtered out; broken-no-prices is dropped (no price fields).
	require.Len(t, resp.Data, 2, "expected 2 valid entries (sample_spec + no-price entry skipped)")

	// Sorted alphabetically by model_id, so claude- < gpt-.
	claude := resp.Data[0]
	assert.Equal(t, "claude-sonnet-4.5", claude.ModelID)
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
	assert.Equal(t, "gpt-4o-mini", gpt.ModelID)
	assert.Equal(t, "openai", gpt.Vendor)
	// supports_function_calling alone also yields tool_use.
	assert.Contains(t, gpt.Capabilities, "tool_use")
	assert.Contains(t, gpt.Capabilities, "vision")
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
