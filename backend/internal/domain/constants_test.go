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

func TestDefaultAntigravityModelMapping_Gemini4ArgonAndClaude55Models(t *testing.T) {
	if got := DefaultAntigravityModelMapping["gemini-4-argon"]; got != "gemini-4-argon-high" {
		t.Fatalf("public argon alias = %q", got)
	}
	floors := AntigravityThinkingWireFloorEntries()
	for _, tier := range []string{"gemini-4-argon-low", "gemini-4-argon-medium", "gemini-4-argon-high"} {
		if floors[tier] != tier {
			t.Fatalf("argon wire floor %s = %q", tier, floors[tier])
		}
		if _, ok := DefaultAntigravityModelMapping[tier]; ok {
			t.Fatalf("argon wire id %s must stay out of the public mapping", tier)
		}
	}
	// Claude 5.5 不进入 Antigravity 公开默认映射；Bedrock 仍保留 Sonnet 5.5。
	for _, id := range []string{"claude-opus-5-5", "claude-opus-5-5-high", "claude-sonnet-5-5", "claude-sonnet-5-5-high"} {
		if _, ok := DefaultAntigravityModelMapping[id]; ok {
			t.Fatalf("claude 5.5 id %s must stay out of the public antigravity mapping", id)
		}
	}
	if got := DefaultBedrockModelMapping["claude-sonnet-5-5"]; got != "global.anthropic.claude-sonnet-5-5" {
		t.Fatalf("bedrock sonnet 5.5 = %q", got)
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
