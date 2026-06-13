package domain

import "testing"

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
