//go:build unit

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCatalogFileSourceReusesBytesAndTracksSourceChanges(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "model_pricing.json")
	fallback := filepath.Join(dir, "fallback.json")
	require.NoError(t, os.WriteFile(live, []byte(`{"live":1}`), 0600))
	require.NoError(t, os.WriteFile(fallback, []byte(`{"fallback":1}`), 0600))
	cfg := &config.Config{}
	cfg.Pricing.DataDir, cfg.Pricing.FallbackFile = dir, fallback
	source := defaultCatalogSource(cfg)
	first, firstMT, ok := source()
	require.True(t, ok)
	second, secondMT, ok := source()
	require.True(t, ok)
	require.Equal(t, firstMT, secondMT)
	require.Same(t, &first[0], &second[0], "unchanged metadata must reuse bytes, not read and allocate the whole file")

	require.NoError(t, os.WriteFile(live, []byte(`{"updated":2}`), 0600))
	changedMT := firstMT.Add(time.Second)
	require.NoError(t, os.Chtimes(live, changedMT, changedMT))
	updated, updatedMT, ok := source()
	require.True(t, ok)
	require.Equal(t, `{"updated":2}`, string(updated))
	require.Equal(t, changedMT, updatedMT)

	// Registry writers use atomic replacement; inode changes must be observed
	// even when a replacement preserves the previous file's size and mtime.
	replacement := filepath.Join(dir, "replacement.json")
	require.NoError(t, os.WriteFile(replacement, []byte(`{"updated":3}`), 0600))
	require.NoError(t, os.Chtimes(replacement, changedMT, changedMT))
	require.NoError(t, os.Rename(replacement, live))
	replaced, _, ok := source()
	require.True(t, ok)
	require.Equal(t, `{"updated":3}`, string(replaced))

	require.NoError(t, os.Remove(live))
	fallbackBody, _, ok := source()
	require.True(t, ok)
	require.Equal(t, `{"fallback":1}`, string(fallbackBody))
	require.NoError(t, os.Remove(fallback))
	missing, _, ok := source()
	require.False(t, ok)
	require.Nil(t, missing)
	require.NoError(t, os.WriteFile(live, []byte(`{"recovered":1}`), 0600))
	recovered, _, ok := source()
	require.True(t, ok)
	require.Equal(t, `{"recovered":1}`, string(recovered))
}

func TestCatalogFileSourceReadFailureFallsBack(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "model_pricing.json")
	fallback := filepath.Join(dir, "fallback.json")
	require.NoError(t, os.WriteFile(live, []byte(`{"live":1}`), 0600))
	require.NoError(t, os.WriteFile(fallback, []byte(`{"fallback":1}`), 0600))
	cfg := &config.Config{}
	cfg.Pricing.DataDir, cfg.Pricing.FallbackFile = dir, fallback
	source := defaultCatalogSource(cfg)
	_, _, ok := source()
	require.True(t, ok)
	require.NoError(t, os.Remove(live))
	require.NoError(t, os.Mkdir(live, 0700))
	body, _, ok := source()
	require.True(t, ok)
	require.Equal(t, `{"fallback":1}`, string(body), "a file that now fails reading must not serve its previously cached bytes")
}

func TestCatalogMembershipAtomicReplacementWithSameMTime(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "model_pricing.json")
	body := `{"test-model-one":{"input_cost_per_token":0.000001,"litellm_provider":"openai"}}`
	require.NoError(t, os.WriteFile(file, []byte(body), 0600))
	info, err := os.Stat(file)
	require.NoError(t, err)
	cfg := &config.Config{}
	cfg.Pricing.DataDir = dir
	svc := NewPricingCatalogService(cfg)
	require.True(t, svc.IsModelPriced("test-model-one", PlatformOpenAI))
	replacement := filepath.Join(dir, "next.json")
	require.NoError(t, os.WriteFile(replacement, []byte(strings.ReplaceAll(body, "one", "two")), 0600))
	require.NoError(t, os.Chtimes(replacement, info.ModTime(), info.ModTime()))
	require.NoError(t, os.Rename(replacement, file))
	require.False(t, svc.IsModelPriced("test-model-one", PlatformOpenAI))
	require.True(t, svc.IsModelPriced("test-model-two", PlatformOpenAI))
}

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
	require.False(t, svc.IsModelPriced("gpt-5.4", "openai"), "malformed source must retire the previous membership index")
	require.NoError(t, os.Remove(file))
	require.False(t, svc.IsModelPriced("gpt-5.4", "openai"))
}

var benchmarkCatalogMembershipFound bool

func BenchmarkCatalogFileMembership(b *testing.B) {
	dir := b.TempDir()
	file := filepath.Join(dir, "model_pricing.json")
	var fixture strings.Builder
	fixture.WriteString("{")
	for i := 0; i < 10000; i++ {
		if i > 0 {
			fixture.WriteByte(',')
		}
		fmt.Fprintf(&fixture, `"benchmark-model-%05d":{"input_cost_per_token":0.000001,"litellm_provider":"openai","mode":"chat"}`, i)
	}
	fixture.WriteByte('}')
	require.NoError(b, os.WriteFile(file, []byte(fixture.String()), 0600))
	cfg := &config.Config{}
	cfg.Pricing.DataDir = dir
	svc := NewPricingCatalogService(cfg)
	svc.BuildPublicCatalog(context.Background())
	b.Run("indexed", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			benchmarkCatalogMembershipFound = svc.IsModelPriced("benchmark-model-09999", "openai")
		}
	})
	b.Run("previous_read_and_scan", func(b *testing.B) {
		legacy := &PricingCatalogService{source: func() ([]byte, time.Time, bool) {
			body, err := os.ReadFile(file)
			if err != nil {
				return nil, time.Time{}, false
			}
			info, err := os.Stat(file)
			if err != nil {
				return body, time.Time{}, true
			}
			return body, info.ModTime(), true
		}}
		legacy.BuildPublicCatalog(context.Background())
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			// Reproduce the old warm-cache path without the new byte-identity check.
			legacy.mu.RLock()
			source := legacy.source
			legacy.mu.RUnlock()
			data, modTime, ok := source()
			if !ok || len(data) == 0 {
				b.Fatal("benchmark catalog source unavailable")
			}
			snapshot := loadTKPricingOverlaySnapshot()
			legacy.mu.RLock()
			catalog, cachedMt, cachedTk := legacy.cached, legacy.cachedMt, legacy.cachedTk
			legacy.mu.RUnlock()
			if catalog == nil || cachedTk != snapshot || modTime.IsZero() || !modTime.Equal(cachedMt) {
				b.Fatal("benchmark requires a warm catalog cache")
			}
			benchmarkCatalogMembershipFound = false
			for j := range catalog.Data {
				if catalog.Data[j].ModelID == "benchmark-model-09999" {
					benchmarkCatalogMembershipFound = true
					break
				}
			}
		}
	})
}
