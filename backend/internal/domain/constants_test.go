package domain

import (
	"strings"
	"testing"
)

func TestDefaultAntigravityModelMapping_ImageCompatibilityAliases(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		// 2.5-flash-image 上游返回 502（2026-06-15 prod 中继实测）→ 全部图片别名收敛到
		// 可服务的 3.1-flash-image。
		"gemini-2.5-flash-image":         "gemini-3.1-flash-image",
		"gemini-2.5-flash-image-preview": "gemini-3.1-flash-image",
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

// TestDefaultAntigravityModelMapping_DeprecatedProRemap 守护 2026-06-15 prod 中继实测
// 暴露的「假成功 / 弃用」Pro 档收敛：gemini-3-pro-* 上游目录已无（返回 200 但 0/0），
// gemini-3.1-pro-high 在上游 deprecatedModelIds（返回 400）。两者及其 preview 别名都必须
// 重指到等价可服务 wire id，不得回退到不可服务的裸名。
func TestDefaultAntigravityModelMapping_DeprecatedProRemap(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"gemini-3-pro-high":      "gemini-pro-agent",
		"gemini-3-pro-low":       "gemini-3.1-pro-low",
		"gemini-3-pro-preview":   "gemini-pro-agent",
		"gemini-3.1-pro-high":    "gemini-pro-agent",
		"gemini-3.1-pro-preview": "gemini-pro-agent",
	}
	for from, want := range cases {
		got, ok := DefaultAntigravityModelMapping[from]
		if !ok {
			t.Fatalf("expected mapping for %q to exist", from)
		}
		if got != want {
			t.Fatalf("unexpected mapping for %q: got %q want %q", from, got, want)
		}
		// 重指目标本身必须是映射内的可服务 wire id（防再次指向不可服务裸名）。
		if _, ok := DefaultAntigravityModelMapping[want]; !ok {
			t.Fatalf("remap target %q for %q is not itself a known servable wire id", want, from)
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
// claude-* / gpt-oss-* 与 structural-dead 兼容别名后的严格子集，且保留可服务
// gemini wire id；没有可靠公开价的模型不得作为规范账号映射写入。
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
		if _, dead := antigravityStructuralDeadModelMappingKeys[k]; dead {
			t.Fatalf("structural-dead antigravity alias leaked into gemini-only map: %q", k)
		}
		// strict subset of the default (same key→value)
		if got, ok := DefaultAntigravityModelMapping[k]; !ok || got != v {
			t.Fatalf("gemini-only entry %q=%q not a faithful subset of default (default has ok=%v val=%q)", k, v, ok, got)
		}
	}
	// representative priced gemini wire ids are retained
	for _, want := range []string{
		"gemini-3.5-flash-low", "gemini-3.5-flash-extra-low", "gemini-3-flash-agent",
		"gemini-pro-agent", "gemini-2.5-flash",
	} {
		if _, ok := m[want]; !ok {
			t.Fatalf("expected gemini-only map to retain %q", want)
		}
	}
	for _, blocked := range []string{"tab_flash_lite_preview"} {
		if _, ok := DefaultAntigravityModelMapping[blocked]; ok {
			t.Fatalf("unpriced antigravity model %q must not remain in default mapping", blocked)
		}
		if _, ok := m[blocked]; ok {
			t.Fatalf("unpriced antigravity model %q must not be written into gemini-only map", blocked)
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

func TestGeminiOnlyAntigravityModelMapping_DropsStructuralDeadAliases(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"gemini-2.5-flash-image-preview",
		"gemini-3-flash-preview",
		"gemini-3-pro-high",
		"gemini-3-pro-image-preview",
		"gemini-3-pro-low",
		"gemini-3-pro-preview",
		"gemini-3.1-pro-high",
		"gemini-3.1-pro-preview",
	} {
		if _, ok := DefaultAntigravityModelMapping[key]; !ok {
			t.Fatalf("default compatibility mapping must retain %q", key)
		}
		if _, ok := GeminiOnlyAntigravityModelMapping[key]; ok {
			t.Fatalf("canonical gemini-only account mapping must drop structural-dead alias %q", key)
		}
	}
	for _, want := range []string{
		"gemini-2.5-flash-image",
		"gemini-3-flash",
		"gemini-3.1-flash-image",
		"gemini-3.1-pro-low",
		"gemini-pro-agent",
	} {
		if _, ok := GeminiOnlyAntigravityModelMapping[want]; !ok {
			t.Fatalf("canonical gemini-only account mapping must keep servable target %q", want)
		}
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
		"claude-sonnet-5": "us.anthropic.claude-sonnet-5-v1",
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
