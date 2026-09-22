package domain

import (
	"slices"
	"sort"
	"testing"
)

func TestDefaultAntigravityModelMapping_ConvergedPublicSurface(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"gemini-3.6-flash":               "gemini-3.6-flash-tiered",
		"gemini-3.7-flash":               "gemini-3.7-flash-high",
		"gemini-3.8-flash":               "gemini-3.8-flash-high",
		"gemini-3-flash":                 "gemini-3.8-flash-high",
		"gemini-3-flash-preview":         "gemini-3.8-flash-high",
		"gemini-3.5-flash-lite":          "gemini-3.6-flash-tiered",
		"gemini-3.1-flash-image":         "gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview": "gemini-3.1-flash-image",
		"gemini-3-pro-image":             "gemini-3.1-flash-image",
		"nano-2":                         "gemini-3.1-flash-image",
		"nano-pro":                       "gemini-3.1-flash-image",
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

func TestDefaultBedrockModelMapping_ContainsNewClaudeModels(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"claude-fable-5":  "anthropic.claude-fable-5",
		"claude-opus-4-8": "us.anthropic.claude-opus-4-8-v1",
		"claude-sonnet-5": "us.anthropic.claude-sonnet-5-v1",
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
