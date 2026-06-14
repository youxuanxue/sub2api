package domain

import (
	"strings"
	"testing"
)

func TestDefaultAntigravityModelMapping_ImageCompatibilityAliases(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"gemini-2.5-flash-image":         "gemini-2.5-flash-image",
		"gemini-2.5-flash-image-preview": "gemini-2.5-flash-image",
		"gemini-3.1-flash-image":         "gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview": "gemini-3.1-flash-image",
		"gemini-3-pro-image":             "gemini-3.1-flash-image",
		"gemini-3-pro-image-preview":     "gemini-3.1-flash-image",
	}

	for from, want := range cases {
		got, ok := DefaultAntigravityModelMapping[from]
		if !ok {
			t.Fatalf("expected mapping for %q to exist", from)
		}
		if got != want {
			t.Fatalf("unexpected mapping for %q: got %q want %q", from, got, want)
		}
	}
}

func TestDefaultAntigravityModelMapping_ContainsNewClaudeModels(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"claude-fable-5":  "claude-fable-5",
		"claude-opus-4-8": "claude-opus-4-8",
	}
	for from, want := range cases {
		got, ok := DefaultAntigravityModelMapping[from]
		if !ok {
			t.Fatalf("expected mapping for %q to exist", from)
		}
		if got != want {
			t.Fatalf("unexpected mapping for %q: got %q want %q", from, got, want)
		}
	}
}

func TestDefaultAntigravityModelMapping_ContainsEmpiricalGeminiWireIDs(t *testing.T) {
	t.Parallel()

	// 2026-06 实测 /v1internal:fetchAvailableModels 的线上 wire id（identity）+ 友好别名。
	// claude 系仍保留在默认映射中（claude 经 anthropic 账号服务；antigravity 账号按需在
	// credentials.model_mapping 排除 claude），此处只断言新增 gemini wire id 已就位。
	cases := map[string]string{
		"gemini-3.5-flash-low":       "gemini-3.5-flash-low",
		"gemini-3.5-flash-extra-low": "gemini-3.5-flash-extra-low",
		"gemini-3-flash-agent":       "gemini-3-flash-agent",
		"gemini-pro-agent":           "gemini-pro-agent",
		"gemini-3.5-flash":           "gemini-3.5-flash-low", // 友好别名 → Medium 档
	}
	for from, want := range cases {
		got, ok := DefaultAntigravityModelMapping[from]
		if !ok {
			t.Fatalf("expected mapping for %q to exist", from)
		}
		if got != want {
			t.Fatalf("unexpected mapping for %q: got %q want %q", from, got, want)
		}
	}
}

// §5.x keep-don't-strip 守卫：gpt-oss-120b-medium 与 claude 一样仅从 antigravity
// 服务面（pricing/allowlist/usage-guide）移除，但必须保留在默认映射里（按账号
// credentials.model_mapping 排除，而非删默认）。与 claude keep-guard 对称，防止
// 未来 merge 静默删掉它而无测试翻红。
// GeminiOnlyAntigravityModelMapping 必须是 DefaultAntigravityModelMapping 去掉
// claude-* / gpt-oss-* 后的严格子集，且保留全部 gemini + tab_flash_lite_preview。
// 这是 AntigravityConfigReconciler 写入账号的规范 gemini-only 映射的单一真值源。
func TestGeminiOnlyAntigravityModelMapping(t *testing.T) {
	t.Parallel()

	m := GeminiOnlyAntigravityModelMapping
	if len(m) == 0 {
		t.Fatal("GeminiOnlyAntigravityModelMapping must not be empty")
	}
	for k, v := range m {
		if strings.HasPrefix(k, "claude-") || strings.HasPrefix(k, "gpt-oss-") {
			t.Fatalf("excluded key leaked into gemini-only map: %q", k)
		}
		// strict subset of the default (same key→value)
		if got, ok := DefaultAntigravityModelMapping[k]; !ok || got != v {
			t.Fatalf("gemini-only entry %q=%q not a faithful subset of default (default has ok=%v val=%q)", k, v, ok, got)
		}
	}
	// representative gemini wire ids + the Google-native tab model are retained
	for _, want := range []string{
		"gemini-3.5-flash-low", "gemini-3.5-flash-extra-low", "gemini-3-flash-agent",
		"gemini-pro-agent", "gemini-2.5-flash", "tab_flash_lite_preview",
	} {
		if _, ok := m[want]; !ok {
			t.Fatalf("expected gemini-only map to retain %q", want)
		}
	}
	// sanity: the default really did contain the excluded families (so we filtered)
	if _, ok := DefaultAntigravityModelMapping["claude-sonnet-4-6"]; !ok {
		t.Fatal("expected default map to contain claude-sonnet-4-6 (filter sanity)")
	}
	if _, ok := m["claude-sonnet-4-6"]; ok {
		t.Fatal("claude-sonnet-4-6 must be excluded from gemini-only map")
	}
	if _, ok := m["gpt-oss-120b-medium"]; ok {
		t.Fatal("gpt-oss-120b-medium must be excluded from gemini-only map")
	}
}

func TestDefaultAntigravityModelMapping_KeepsGptOss(t *testing.T) {
	t.Parallel()

	got, ok := DefaultAntigravityModelMapping["gpt-oss-120b-medium"]
	if !ok {
		t.Fatalf("expected gpt-oss-120b-medium to remain in DefaultAntigravityModelMapping (§5.x keep-don't-strip)")
	}
	if got != "gpt-oss-120b-medium" {
		t.Fatalf("unexpected mapping for gpt-oss-120b-medium: got %q want identity", got)
	}
}

func TestDefaultBedrockModelMapping_ContainsNewClaudeModels(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"claude-fable-5":  "anthropic.claude-fable-5",
		"claude-opus-4-8": "us.anthropic.claude-opus-4-8-v1",
	}
	for from, want := range cases {
		got, ok := DefaultBedrockModelMapping[from]
		if !ok {
			t.Fatalf("expected Bedrock mapping for %q to exist", from)
		}
		if got != want {
			t.Fatalf("unexpected Bedrock mapping for %q: got %q want %q", from, got, want)
		}
	}
}
