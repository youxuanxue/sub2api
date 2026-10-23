package domain

import (
	"slices"
	"sort"
	"testing"
)

func TestDefaultAntigravityModelMapping_ConvergedPublicSurface(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"gemini-3.7-flash":               "gemini-3.7-flash-high",
		"gemini-3.8-flash":               "gemini-3.8-flash-high",
		"gemini-4-argon":                 "gemini-4-argon-high",
		"gemini-3-flash":                 "gemini-3.8-flash-high",
		"gemini-3-flash-preview":         "gemini-3.8-flash-high",
		"gemini-3.1-flash-image":         "gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview": "gemini-3.1-flash-image",
		"nano-2":                         "gemini-3.1-flash-image",
		"nano-banana-2":                  "gemini-3.1-flash-image",
	}
	if len(DefaultAntigravityModelMapping) != len(want) {
		t.Fatalf("size got %d want %d (%v)", len(DefaultAntigravityModelMapping), len(want), DefaultAntigravityModelMapping)
	}
	for from, to := range want {
		got, ok := DefaultAntigravityModelMapping[from]
		if !ok || got != to {
			t.Fatalf("%q: got %q/%v want %q", from, got, ok, to)
		}
	}
}

func TestDefaultAntigravityModelMapping_DropsRetiredFamilies(t *testing.T) {
	t.Parallel()
	for _, retired := range []string{"claude-sonnet-4-6", "gemini-2.5-flash", "gemini-pro-agent"} {
		if _, ok := DefaultAntigravityModelMapping[retired]; ok {
			t.Fatalf("retired %q still present", retired)
		}
	}
}

func TestDefaultAntigravityModelMapping_DropsUnpricedAndUnsupportedFamilies(t *testing.T) {
	t.Parallel()
	for _, blocked := range append(AntigravityUnpricedModelMappingKeys(), "gpt-oss-120b-medium") {
		if _, ok := DefaultAntigravityModelMapping[blocked]; ok {
			t.Fatalf("blocked %q still present", blocked)
		}
	}
}

func TestDefaultAntigravityModelMapping_DropsStructuralDeadAliases(t *testing.T) {
	t.Parallel()
	for _, key := range AntigravityStructuralDeadModelMappingKeys() {
		if _, ok := DefaultAntigravityModelMapping[key]; ok {
			t.Fatalf("dead alias %q still present", key)
		}
	}
}

func TestAntigravityBlockedModelMappingKeyExports(t *testing.T) {
	t.Parallel()
	if !slices.Equal(AntigravityStructuralDeadModelMappingKeys(), sortedModelMappingKeysForTest(antigravityStructuralDeadModelMappingKeys)) {
		t.Fatal("structural export mismatch")
	}
	if !slices.Equal(AntigravityUnpricedModelMappingKeys(), sortedModelMappingKeysForTest(antigravityUnpricedModelMappingKeys)) {
		t.Fatal("unpriced export mismatch")
	}
}

func TestAntigravityBlockedModelMappingPredicatesMatchOwners(t *testing.T) {
	t.Parallel()
	for key := range antigravityStructuralDeadModelMappingKeys {
		if !IsAntigravityStructuralDeadModelMappingKey(key) {
			t.Fatalf("missing structural predicate %q", key)
		}
	}
	for key := range antigravityUnpricedModelMappingKeys {
		if !IsAntigravityUnpricedModelMappingKey(key) {
			t.Fatalf("missing unpriced predicate %q", key)
		}
	}
}

// TestDefaultAntigravityModelMapping_Gemini4ArgonTiersAndClaude55Convergence
// 锚定 TokenKey 的 Antigravity 请求面划分（上游把三类键全部塞进
// DefaultAntigravityModelMapping，TokenKey 不是）：
//
//   - 公开裸名（gemini-4-argon）在默认映射里锁 high wire 档位；
//   - -low/-medium/-high 是非公开 wire 档位，owner 是
//     AntigravityThinkingWireFloorEntries()，不进默认映射（否则会被
//     /v1/models 与定价菜单当成公开模型广告出去）；
//   - Claude 系（含 5.5）按 #2207 收敛后只走 Gemini 公开面，默认映射里
//     必须没有 Claude 键；它们仅通过账号自定义 model_mapping 可达
//     （display name 由 antigravity.ModelMetadata() 提供）。
func TestDefaultAntigravityModelMapping_Gemini4ArgonTiersAndClaude55Convergence(t *testing.T) {
	t.Parallel()

	// 裸名默认锁 high wire（非 medium），与 Flash 家族一致。
	if got := DefaultAntigravityModelMapping["gemini-4-argon"]; got != "gemini-4-argon-high" {
		t.Fatalf("expected gemini-4-argon to map to %q, got %q", "gemini-4-argon-high", got)
	}

	// Argon wire 档位由 thinking wire floor owner 以 identity 提供，且不得回流默认映射。
	wires := AntigravityThinkingWireFloorEntries()
	for _, id := range []string{"gemini-4-argon-low", "gemini-4-argon-medium", "gemini-4-argon-high"} {
		if got := wires[id]; got != id {
			t.Fatalf("expected wire floor identity for %s, got %q", id, got)
		}
		if !IsAntigravityThinkingWireModel(id) {
			t.Fatalf("expected %s to be reported as a non-public AG thinking wire model", id)
		}
		if got, ok := DefaultAntigravityModelMapping[id]; ok {
			t.Fatalf("wire tier %s must stay out of the default mapping (public surface), got %q", id, got)
		}
	}

	// 收敛后的公开面只有 Gemini：Claude 键不得出现在默认映射里。
	for _, id := range []string{
		"claude-opus-5-5", "claude-opus-5-5-high", "claude-opus-5-5-low", "claude-opus-5-5-medium",
		"claude-sonnet-5-5", "claude-sonnet-5-5-high", "claude-sonnet-5-5-low", "claude-sonnet-5-5-medium",
	} {
		if got, ok := DefaultAntigravityModelMapping[id]; ok {
			t.Fatalf("converged Antigravity surface must not default-map Claude id %s, got %q", id, got)
		}
	}
}

func TestDefaultBedrockModelMapping_ContainsNewClaudeModels(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"claude-fable-5":    "anthropic.claude-fable-5",
		"claude-opus-4-8":   "us.anthropic.claude-opus-4-8-v1",
		"claude-sonnet-5":   "us.anthropic.claude-sonnet-5-v1",
		"claude-fable-5-1":  "anthropic.claude-fable-5-1",
		"claude-sonnet-5-5": "global.anthropic.claude-sonnet-5-5",
	}
	for from, want := range cases {
		got, ok := DefaultBedrockModelMapping[from]
		if !ok || got != want {
			t.Fatalf("%q got %q/%v want %q", from, got, ok, want)
		}
	}
}

func sortedModelMappingKeysForTest(src map[string]struct{}) []string {
	out := make([]string, 0, len(src))
	for k := range src {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
