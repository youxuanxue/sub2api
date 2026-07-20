//go:build unit

package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// embeddedQwen8bInput is the embedded floor price for qwen3-8b — the assertion
// anchor for "runtime overrides embedded" / "corrupt keeps embedded floor".
const embeddedQwen8bInput = 7.462686567164179e-08

func resetOverlayUnion(t *testing.T) {
	t.Helper()
	rebuildTKOverlayUnion(nil) // embedded-only floor
	t.Cleanup(func() { rebuildTKOverlayUnion(nil) })
}

func TestRebuildTKOverlayUnion_RuntimeWins(t *testing.T) {
	resetOverlayUnion(t)

	// Sanity: embedded floor carries qwen3-8b at the known price.
	if got := loadTKPricingOverlay()["qwen3-8b"]; got == nil || got.InputCostPerToken != embeddedQwen8bInput {
		t.Fatalf("embedded floor qwen3-8b = %+v, want input %g", got, embeddedQwen8bInput)
	}

	runtime := `{
        "qwen3-8b": {"input_cost_per_token": 9.99e-06, "litellm_provider": "dashscope", "mode": "chat"},
        "zz-runtime-only": {"input_cost_per_token": 1.0e-06, "output_cost_per_token": 2.0e-06, "litellm_provider": "dashscope", "mode": "chat"}
    }`
	rebuildTKOverlayUnion([]byte(runtime))
	eff := loadTKPricingOverlay()

	if eff["qwen3-8b"].InputCostPerToken != 9.99e-06 {
		t.Errorf("runtime did not win for qwen3-8b: got %g", eff["qwen3-8b"].InputCostPerToken)
	}
	if eff["zz-runtime-only"] == nil {
		t.Errorf("runtime-only key missing from union")
	}
	// An embedded-only key (not in runtime) is still present.
	if eff["qwen3-32b"] == nil {
		t.Errorf("embedded-only key qwen3-32b dropped from union")
	}
}

func TestRebuildTKOverlayUnion_CorruptRuntimeKeepsEmbeddedFloor(t *testing.T) {
	resetOverlayUnion(t)
	rebuildTKOverlayUnion([]byte("{ this is not valid json"))
	eff := loadTKPricingOverlay()
	if eff["qwen3-8b"] == nil || eff["qwen3-8b"].InputCostPerToken != embeddedQwen8bInput {
		t.Fatalf("corrupt runtime must keep embedded floor; got %+v", eff["qwen3-8b"])
	}
}

func TestRebuildTKOverlayUnion_CorruptFirstLoadEstablishesEmbeddedFloor(t *testing.T) {
	tkOverlayMu.Lock()
	tkOverlayEffective = nil
	tkOverlayMu.Unlock()
	t.Cleanup(func() { rebuildTKOverlayUnion(nil) })

	rebuildTKOverlayUnion([]byte("{ this is not valid json"))
	eff := loadTKPricingOverlay()
	require.NotNil(t, eff["qwen3-8b"])
	require.InDelta(t, embeddedQwen8bInput, eff["qwen3-8b"].InputCostPerToken, 1e-18)
}

func TestRebuildTKOverlayUnion_EmptyFallsBackToEmbedded(t *testing.T) {
	resetOverlayUnion(t)
	rebuildTKOverlayUnion([]byte(`{"qwen3-8b":{"input_cost_per_token":5e-06,"litellm_provider":"dashscope"}}`))
	if loadTKPricingOverlay()["qwen3-8b"].InputCostPerToken != 5e-06 {
		t.Fatal("setup: runtime override not applied")
	}
	rebuildTKOverlayUnion(nil) // operator cleared the key
	if loadTKPricingOverlay()["qwen3-8b"].InputCostPerToken != embeddedQwen8bInput {
		t.Fatal("empty runtime must fall back to embedded floor")
	}
}

func runtimeTaxPolicyBlob(t *testing.T, multiplier float64, dropFirstProvider bool) string {
	t.Helper()
	policy := loadTkOfficialListBaseTaxPolicy()
	policy.Multiplier = multiplier
	if dropFirstProvider {
		require.NotEmpty(t, policy.Rules)
		policy.Rules = policy.Rules[1:]
	}
	payload, err := json.Marshal(map[string]any{
		"_config": map[string]any{"official_list_base_tax": policy},
	})
	require.NoError(t, err)
	return string(payload)
}

func TestRebuildTKOverlayUnion_RuntimeTaxPolicyWinsAndModelOnlyInheritsFloor(t *testing.T) {
	resetOverlayUnion(t)
	embeddedMultiplier := tkOfficialListBaseTaxMultiplier()
	runtime := runtimeTaxPolicyBlob(t, 1.07, false)
	rebuildTKOverlayUnion([]byte(runtime))
	require.InDelta(t, 1.07, tkOfficialListBaseTaxMultiplier(), 1e-12)
	for _, rule := range loadTkOfficialListBaseTaxPolicy().Rules {
		litellm := tkPresentLiteLLMModelPricing(&LiteLLMModelPricing{
			LiteLLMProvider:    rule.Provider,
			InputCostPerToken:  1,
			OutputCostPerToken: 2,
		})
		require.InDelta(t, 1.07, litellm.InputCostPerToken, 1e-12, rule.Provider)

		catalog := PublicCatalogPricing{InputPer1KTokens: 1, OutputPer1KTokens: 2}
		tkApplyBaseTaxToPublicCatalogPricing(rule.Provider, &catalog)
		require.InDelta(t, 1.07, catalog.InputPer1KTokens, 1e-12, rule.Provider)

		fallback := tkApplyOfficialListBaseTaxForModel(
			sampleModelForTaxRule(t, rule),
			&ModelPricing{InputPricePerToken: 1, OutputPricePerToken: 2},
		)
		require.InDelta(t, 1.07, fallback.InputPricePerToken, 1e-12, rule.Provider)
	}

	// A model-only runtime blob inherits the embedded policy rather than clearing it.
	rebuildTKOverlayUnion([]byte(`{"qwen3-8b":{"input_cost_per_token":5e-06,"litellm_provider":"dashscope"}}`))
	require.InDelta(t, embeddedMultiplier, tkOfficialListBaseTaxMultiplier(), 1e-12)
}

func newTestPricingService() *PricingService {
	return NewPricingService(&config.Config{}, nil)
}

func TestReloadTKOverlayRuntime_HashGatedAndApplies(t *testing.T) {
	resetOverlayUnion(t)
	s := newTestPricingService()
	blob := `{"qwen3-8b":{"input_cost_per_token":8.88e-06,"litellm_provider":"dashscope"}}`
	s.SetOverlayRuntimeDeps(func(context.Context) (string, bool) { return blob, true }, nil)

	changed, err := s.reloadTKOverlayRuntime(context.Background())
	if err != nil || !changed {
		t.Fatalf("first reload changed=%v err=%v, want true/nil", changed, err)
	}
	if loadTKPricingOverlay()["qwen3-8b"].InputCostPerToken != 8.88e-06 {
		t.Errorf("reload did not apply runtime override")
	}
	// Second reload, same blob → hash-gated no-op.
	changed, err = s.reloadTKOverlayRuntime(context.Background())
	if err != nil || changed {
		t.Errorf("second reload changed=%v err=%v, want false/nil (hash-gated)", changed, err)
	}
}

func TestReloadTKOverlayRuntime_CorruptKeepsCurrent(t *testing.T) {
	resetOverlayUnion(t)
	s := newTestPricingService()
	good := `{"qwen3-8b":{"input_cost_per_token":7.77e-06,"litellm_provider":"dashscope"}}`
	cur := good
	s.SetOverlayRuntimeDeps(func(context.Context) (string, bool) { return cur, true }, nil)
	if _, err := s.reloadTKOverlayRuntime(context.Background()); err != nil {
		t.Fatalf("good reload failed: %v", err)
	}
	// Now the getter returns garbage: reload must error and keep the prior value.
	cur = "{ broken"
	changed, err := s.reloadTKOverlayRuntime(context.Background())
	if err == nil || changed {
		t.Errorf("corrupt reload changed=%v err=%v, want false/error", changed, err)
	}
	if loadTKPricingOverlay()["qwen3-8b"].InputCostPerToken != 7.77e-06 {
		t.Errorf("corrupt reload must not disturb the live map; got %g",
			loadTKPricingOverlay()["qwen3-8b"].InputCostPerToken)
	}
}

func TestReloadTKOverlayRuntime_RejectsTaxPolicyThatDropsEmbeddedProvider(t *testing.T) {
	resetOverlayUnion(t)
	s := newTestPricingService()
	blob := runtimeTaxPolicyBlob(t, 1.07, true)
	s.SetOverlayRuntimeDeps(func(context.Context) (string, bool) { return blob, true }, nil)

	changed, err := s.reloadTKOverlayRuntime(context.Background())
	require.Error(t, err)
	require.False(t, changed)
	require.InDelta(t, 1.06, tkOfficialListBaseTaxMultiplier(), 1e-12)
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

func TestReloadTKOverlayRuntime_EmptyGetterIsEmbeddedFloor(t *testing.T) {
	resetOverlayUnion(t)
	s := newTestPricingService()
	s.SetOverlayRuntimeDeps(func(context.Context) (string, bool) { return "", false }, nil)
	if _, err := s.reloadTKOverlayRuntime(context.Background()); err != nil {
		t.Fatalf("empty getter reload err: %v", err)
	}
	if loadTKPricingOverlay()["qwen3-8b"].InputCostPerToken != embeddedQwen8bInput {
		t.Error("empty getter must serve embedded floor")
	}
}

// TestConcurrentOverlayReadDuringSwap exercises the RWMutex hot-swap under -race.
func TestConcurrentOverlayReadDuringSwap(t *testing.T) {
	resetOverlayUnion(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = loadTKPricingOverlay()["qwen3-8b"]
					_ = tkOfficialListBaseTaxMultiplier()
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		rebuildTKOverlayUnion([]byte(`{"qwen3-8b":{"input_cost_per_token":1e-06,"litellm_provider":"dashscope"}}`))
	}
	close(stop)
	wg.Wait()
}

// TestCatalogInvalidateCache_PicksUpHotOverlay is the mtime-cache-trap regression:
// the catalog caches by source mtime, so an overlay-only change is invisible until
// InvalidateCache is called.
func TestCatalogInvalidateCache_PicksUpHotOverlay(t *testing.T) {
	resetOverlayUnion(t)
	cat := NewPricingCatalogService(nil)
	src := []byte(`{"real-model":{"input_cost_per_token":1e-06,"output_cost_per_token":2e-06,"litellm_provider":"openai","mode":"chat"}}`)
	fixedMt := time.Unix(1_700_000_000, 0)
	cat.SetSourceForTesting(func() ([]byte, time.Time, bool) { return src, fixedMt, true })

	const hot = "zz-hot-test-model"
	has := func(resp *PublicCatalogResponse) bool {
		for i := range resp.Data {
			if resp.Data[i].ModelID == hot {
				return true
			}
		}
		return false
	}

	if has(cat.BuildPublicCatalog(context.Background())) {
		t.Fatalf("setup: %s should not exist before hot-push", hot)
	}
	// Hot-push: add the model to the runtime union. Use openai vendor so the
	// newapi long-tail manifest gate (dashscope/alibaba/etc.) does not block this
	// overlay-runtime regression — this test is about mtime cache + InvalidateCache,
	// not manifest membership.
	rebuildTKOverlayUnion([]byte(`{"` + hot + `":{"input_cost_per_token":3e-06,"output_cost_per_token":6e-06,"litellm_provider":"openai","mode":"chat"}}`))

	// Same mtime → still cached, the trap: new model NOT visible.
	if has(cat.BuildPublicCatalog(context.Background())) {
		t.Errorf("expected stale cache (mtime unchanged) to hide %s — trap not reproduced", hot)
	}
	// The fix: invalidate, then it appears.
	cat.InvalidateCache()
	if !has(cat.BuildPublicCatalog(context.Background())) {
		t.Errorf("after InvalidateCache, %s must appear in catalog", hot)
	}
}

// guard against accidental JSON shape drift in the embedded overlay used as floor.
func TestEmbeddedOverlayParsesAsFloor(t *testing.T) {
	resetOverlayUnion(t)
	m, err := parseTKOverlayBytes(tkPricingOverlayRaw)
	if err != nil {
		t.Fatalf("embedded overlay must parse: %v", err)
	}
	if len(m) == 0 {
		t.Fatal("embedded overlay parsed to zero entries")
	}
	doc, err := parseTKOverlayDocument(tkPricingOverlayRaw)
	require.NoError(t, err)
	require.NotNil(t, doc.BaseTax)
	require.NoError(t, doc.BaseTax.validate())
	// _meta / provenance keys are skipped.
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(tkPricingOverlayRaw, &raw)
	if _, ok := m["_meta"]; ok {
		t.Error("provenance key _meta leaked into parsed overlay")
	}
}
