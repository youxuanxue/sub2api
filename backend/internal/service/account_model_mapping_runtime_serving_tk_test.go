//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func resetAccountModelMappingRuntimeServingForTest() {
	accountModelMappingRuntimeServingMu.Lock()
	defer accountModelMappingRuntimeServingMu.Unlock()
	accountModelMappingRuntimeServing.Store(nil)
	accountModelMappingRuntimeServingVer.Store(0)
	accountModelMappingRuntimeServingHash.Store("")
}

func TestAntigravityEffectiveDefaultModelMapping_EmptyRuntimeUsesCompiledDefault(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	account := &Account{Platform: PlatformAntigravity}
	mapping := account.GetModelMapping()
	if mapping["gemini-3.8-flash"] != domain.DefaultAntigravityModelMapping["gemini-3.8-flash"] {
		t.Fatalf("compiled gemini-3.8-flash mapping = %q", mapping["gemini-3.8-flash"])
	}
	if mapping["gemini-3.8-flash-low"] != "gemini-3.8-flash-low" {
		t.Fatalf("thinking wire floor missing: gemini-3.8-flash-low = %q", mapping["gemini-3.8-flash-low"])
	}
	if mapping["gemini-3.8-flash"] != "gemini-3.8-flash-high" {
		t.Fatalf("bare default must lock high, got %q", mapping["gemini-3.8-flash"])
	}
	if !account.IsModelSupported("gemini-3.8-flash") {
		t.Fatal("compiled default must keep existing models servable")
	}
	if mapAntigravityModel(account, "gemini-9.9-flash") != "" {
		t.Fatal("unknown model must stay rejected without a runtime overlay")
	}
}

func TestAntigravityEffectiveDefaultModelMapping_RuntimeOverlaysCompiledDefault(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	blob, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {
				"gemini-9.9-flash": "gemini-9.9-flash-medium",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(blob), true
	}); err != nil {
		t.Fatal(err)
	}

	account := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{}}
	if got := mapAntigravityModel(account, "gemini-9.9-flash"); got != "gemini-9.9-flash-medium" {
		t.Fatalf("runtime overlay mapped %q, want gemini-9.9-flash-medium", got)
	}
	if !account.IsModelSupported("gemini-3.8-flash") {
		t.Fatal("partial runtime overlay must not drop compiled models")
	}
	if mapAntigravityModel(account, "gemini-3.8-flash") == "" {
		t.Fatal("compiled gemini-3.8-flash must remain mapped after overlay")
	}
}

func TestOverlayAntigravityDefaultModelMapping_RuntimeOverrideWinsOnConflict(t *testing.T) {
	compiled := domain.DefaultAntigravityModelMapping["gemini-3.8-flash"]
	if compiled == "" {
		t.Fatal("compiled default must contain gemini-3.8-flash")
	}
	got := overlayAntigravityDefaultModelMapping(map[string]string{
		"gemini-3.8-flash": "gemini-3.8-flash-override",
	})
	if got["gemini-3.8-flash"] != "gemini-3.8-flash-override" {
		t.Fatalf("runtime override mapped %q", got["gemini-3.8-flash"])
	}
	if got["gemini-3.5-flash"] != domain.DefaultAntigravityModelMapping["gemini-3.5-flash"] {
		t.Fatal("override of one key must keep other compiled keys")
	}
}

func TestAntigravityEffectiveDefaultModelMapping_ExplicitAccountMappingWins(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	blob, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {
				"gemini-9.9-flash": "gemini-9.9-flash-medium",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(blob), true
	}); err != nil {
		t.Fatal(err)
	}

	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gemini-3.8-flash": "gemini-3.8-flash-high",
			},
		},
	}
	if account.IsModelSupported("gemini-9.9-flash") {
		t.Fatal("non-empty account mapping must not inherit runtime extras")
	}
	if !account.IsModelSupported("gemini-3.8-flash") {
		t.Fatal("explicit account mapping must stay authoritative")
	}
	if account.IsModelSupported("gemini-3.5-flash") {
		t.Fatal("explicit account mapping must not be auto-filled from compiled or runtime defaults")
	}
}

func TestAntigravityEffectiveDefaultModelMapping_CorruptRuntimeKeepsCompiledDefault(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	good, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {"gemini-9.9-flash": "gemini-9.9-flash-medium"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(good), true
	}); err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return "{not-json", true
	}); err == nil {
		t.Fatal("corrupt blob must fail closed")
	}

	account := &Account{Platform: PlatformAntigravity}
	if got := mapAntigravityModel(account, "gemini-9.9-flash"); got != "gemini-9.9-flash-medium" {
		t.Fatalf("corrupt reload must keep last-known-good overlay, got %q", got)
	}
}

func TestAntigravityEffectiveDefaultModelMapping_ClearRuntimeReturnsCompiledDefault(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	blob, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {"gemini-9.9-flash": "gemini-9.9-flash-medium"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(blob), true
	}); err != nil {
		t.Fatal(err)
	}
	account := &Account{Platform: PlatformAntigravity}
	first := account.GetModelMapping()
	if first["gemini-9.9-flash"] != "gemini-9.9-flash-medium" {
		t.Fatalf("setup overlay missing, got %q", first["gemini-9.9-flash"])
	}

	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return "", false
	}); err != nil {
		t.Fatal(err)
	}
	second := account.GetModelMapping()
	if _, ok := second["gemini-9.9-flash"]; ok {
		t.Fatal("clear-runtime must drop overlay keys and return to compiled default")
	}
	if !account.IsModelSupported("gemini-3.8-flash") {
		t.Fatal("compiled default must remain after clear-runtime")
	}
}

func TestAntigravityEffectiveDefaultModelMapping_CacheInvalidatesOnRuntimeReload(t *testing.T) {
	resetAccountModelMappingRuntimeServingForTest()
	t.Cleanup(resetAccountModelMappingRuntimeServingForTest)

	account := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{}}
	if account.IsModelSupported("gemini-9.9-flash") {
		t.Fatal("pre-reload unknown model must be rejected")
	}

	blob, err := json.Marshal(accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"antigravity": {"gemini-9.9-flash": "gemini-9.9-flash-medium"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloadAccountModelMappingRuntimeServing(context.Background(), func(context.Context) (string, bool) {
		return string(blob), true
	}); err != nil {
		t.Fatal(err)
	}
	if !account.IsModelSupported("gemini-9.9-flash") {
		t.Fatal("same account object must observe the overlay after runtime version bump")
	}
}

// 档位 wire 要注入，裸名自映射不要。专家复审指出这项改动原本没有测试保护：
// 现有两个 Antigravity 测试只断言「不越出 catalog」，删掉裸名注入它们仍然通过。
func TestAntigravityPassthroughsInjectTierWiresNotBareNames(t *testing.T) {
	t.Parallel()
	// 一条显式键即可触发注入（判据是 mapping 里有 gemini-/nano- 前缀键）。
	got := ensureAntigravityDefaultPassthroughs(map[string]string{
		"gemini-3.8-flash": "gemini-3.8-flash-high",
	})

	// 档位 wire 必须在：resolveGeminiThinkingVariantForLevel 要靠它派发档位。
	for _, wire := range []string{
		"gemini-3.7-flash-low", "gemini-3.7-flash-medium", "gemini-3.7-flash-high",
		"gemini-3.8-flash-low", "gemini-3.8-flash-medium", "gemini-3.8-flash-high",
		"gemini-4-argon-low", "gemini-4-argon-medium", "gemini-4-argon-high",
	} {
		require.Equal(t, wire, got[wire], "档位 wire %s 必须被注入", wire)
	}

	// 裸名自映射不得再被注入。它指向上游目录里不存在的名字（AG 只有档位变体），
	// 是 404 的来源，且与 DefaultAntigravityModelMapping 的「裸名锁 -high」相反。
	for _, bare := range []string{"gemini-3.7-flash", "gemini-4-argon"} {
		require.NotEqual(t, bare, got[bare], "裸名 %s 不得自映射", bare)
	}

	// 已有的显式键不被覆盖。
	require.Equal(t, "gemini-3.8-flash-high", got["gemini-3.8-flash"])

	// 已退役的模型不得借注入复活。
	for _, retired := range []string{"gemini-3.6-flash", "gemini-3.5-flash-lite"} {
		require.NotContains(t, got, retired, "已退役的 %s 不得出现", retired)
	}
}
