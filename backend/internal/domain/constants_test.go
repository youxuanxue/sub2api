package domain

import (
	"sort"
	"testing"
)

func TestDefaultAntigravityModelMapping_ImageCompatibilityAliases(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		// 2.5-flash-image 上游返回 502（2026-06-15 prod 中继实测）→ 全部图片别名收敛到
		// 可服务的 3.1-flash-image。
		"gemini-2.5-flash-image":         "gemini-3.1-flash-image",
		"gemini-3.1-flash-image":         "gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview": "gemini-3.1-flash-image",
		"gemini-3-pro-image":             "gemini-3.1-flash-image",
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

	if got := DefaultAntigravityModelMapping["gemini-3.1-pro"]; got != AntigravityGemini31ProAgentModel {
		t.Fatalf("gemini-3.1-pro must map to %q, got %q", AntigravityGemini31ProAgentModel, got)
	}
	for _, dead := range []string{
		"gemini-3-pro-high",
		"gemini-3-pro-low",
		"gemini-3-pro-preview",
		"gemini-3.1-pro-high",
		"gemini-3.1-pro-preview",
	} {
		if _, ok := DefaultAntigravityModelMapping[dead]; ok {
			t.Fatalf("deprecated/structural-dead Antigravity alias %q must not remain in default mapping", dead)
		}
	}
}

func TestDefaultAntigravityModelMapping_ContainsLiveAntigravityClaudeModels(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"claude-sonnet-4-6":        "claude-sonnet-4-6",
		"claude-opus-4-6":          "claude-opus-4-6-thinking",
		"claude-opus-4-6-thinking": "claude-opus-4-6-thinking",
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
	for _, unavailable := range []string{
		"claude-fable-5",
		"claude-opus-4-8",
		"claude-sonnet-5",
		"claude-haiku-4-5",
	} {
		if _, ok := DefaultAntigravityModelMapping[unavailable]; ok {
			t.Fatalf("antigravity default mapping must not expose live-unavailable Claude model %q", unavailable)
		}
	}
}

func TestDefaultAntigravityModelMapping_ContainsEmpiricalGeminiWireIDs(t *testing.T) {
	t.Parallel()

	// 2026-06 实测 /v1internal:fetchAvailableModels 的线上 wire id（identity）+ 友好别名。
	// Claude 由单独测试守护 live 子集；此处只断言新增 gemini wire id 已就位。
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

func TestDefaultAntigravityModelMapping_Gemini31ProAliases(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		AntigravityGemini31ProAgentModel: AntigravityGemini31ProAgentModel,
		"gemini-3.1-pro":                 AntigravityGemini31ProAgentModel,
		"gemini-3.1-pro-low":             "gemini-3.1-pro-low",
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

func TestDefaultAntigravityModelMapping_DropsUnpricedAndUnsupportedFamilies(t *testing.T) {
	t.Parallel()

	for _, blocked := range []string{"tab_flash_lite_preview", "gpt-oss-120b-medium"} {
		if _, ok := DefaultAntigravityModelMapping[blocked]; ok {
			t.Fatalf("unsupported/unpriced antigravity model %q must not remain in default mapping", blocked)
		}
	}
}

func TestDefaultAntigravityModelMapping_DropsStructuralDeadAliases(t *testing.T) {
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
		if _, ok := DefaultAntigravityModelMapping[key]; ok {
			t.Fatalf("default mapping must drop structural-dead alias %q", key)
		}
	}
	for _, want := range []string{
		"gemini-2.5-flash-image",
		"gemini-3-flash",
		"gemini-3.1-flash-image",
		"gemini-3.1-pro-low",
		"gemini-pro-agent",
	} {
		if _, ok := DefaultAntigravityModelMapping[want]; !ok {
			t.Fatalf("default mapping must keep servable target %q", want)
		}
	}
}

func TestAntigravityBlockedModelMappingKeyExports(t *testing.T) {
	t.Parallel()

	structural := AntigravityStructuralDeadModelMappingKeys()
	if !sort.StringsAreSorted(structural) {
		t.Fatalf("structural-dead export must be sorted: %v", structural)
	}
	if !containsString(structural, "gemini-3-pro-high") {
		t.Fatalf("structural-dead export must include gemini-3-pro-high")
	}
	unpriced := AntigravityUnpricedModelMappingKeys()
	if !sort.StringsAreSorted(unpriced) {
		t.Fatalf("unpriced export must be sorted: %v", unpriced)
	}
	if !containsString(unpriced, "tab_flash_lite_preview") {
		t.Fatalf("unpriced export must include tab_flash_lite_preview")
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

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
