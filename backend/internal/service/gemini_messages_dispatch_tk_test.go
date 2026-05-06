//go:build unit

package service

import "testing"

// TestTKResolveGeminiDispatchModel 钉住 gemini 分组级 Claude→Gemini 模型
// 映射的所有边界。运维在 admin UI 配 OpusMappedModel / SonnetMappedModel /
// HaikuMappedModel / ExactModelMappings；resolver 在 /v1/messages → gemini
// 桥接 Forward() 路径上替换 req.Model。空配置 / gemini-* 形态 / nil group
// 等无操作 case 必须 passthrough（返回 ""），让上游 404 暴露真实状态。
func TestTKResolveGeminiDispatchModel(t *testing.T) {
	cfg := OpenAIMessagesDispatchModelConfig{
		OpusMappedModel:   "gemini-2.5-pro",
		SonnetMappedModel: "gemini-2.5-flash",
		HaikuMappedModel:  "gemini-2.0-flash",
		ExactModelMappings: map[string]string{
			"claude-sonnet-4-5-20250929": "gemini-3.1-pro-preview",
		},
	}
	groupWith := func(c OpenAIMessagesDispatchModelConfig) *Group {
		return &Group{Platform: PlatformGemini, MessagesDispatchModelConfig: c}
	}

	cases := []struct {
		name     string
		group    *Group
		input    string
		expected string
	}{
		{
			name:     "exact mapping wins over family",
			group:    groupWith(cfg),
			input:    "claude-sonnet-4-5-20250929",
			expected: "gemini-3.1-pro-preview",
		},
		{
			name:     "opus family fallback",
			group:    groupWith(cfg),
			input:    "claude-3-5-opus-20250101",
			expected: "gemini-2.5-pro",
		},
		{
			name:     "sonnet family fallback",
			group:    groupWith(cfg),
			input:    "claude-3-5-sonnet-20250101",
			expected: "gemini-2.5-flash",
		},
		{
			name:     "haiku family fallback",
			group:    groupWith(cfg),
			input:    "claude-3-5-haiku-20250101",
			expected: "gemini-2.0-flash",
		},
		{
			name:     "empty config = passthrough",
			group:    groupWith(OpenAIMessagesDispatchModelConfig{}),
			input:    "claude-3-5-opus-20250101",
			expected: "",
		},
		{
			name:     "gemini-prefixed input = passthrough (no self-remap)",
			group:    groupWith(cfg),
			input:    "gemini-3.1-pro-preview",
			expected: "",
		},
		{
			name:     "case-insensitive gemini prefix",
			group:    groupWith(cfg),
			input:    "Gemini-2.5-Pro",
			expected: "",
		},
		{
			name:     "nil group",
			group:    nil,
			input:    "claude-3-5-opus-20250101",
			expected: "",
		},
		{
			name:     "empty model",
			group:    groupWith(cfg),
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace-only model",
			group:    groupWith(cfg),
			input:    "   ",
			expected: "",
		},
		{
			name:     "unknown family with no exact match",
			group:    groupWith(cfg),
			input:    "gpt-4-turbo",
			expected: "",
		},
		{
			name: "exact mapping with whitespace-only target = no map (treats as unset)",
			group: groupWith(OpenAIMessagesDispatchModelConfig{
				OpusMappedModel: "gemini-2.5-pro",
				ExactModelMappings: map[string]string{
					"claude-3-opus-20240229": "   ",
				},
			}),
			input:    "claude-3-opus-20240229",
			expected: "gemini-2.5-pro", // exact had only whitespace → falls through to opus family
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.group.TKResolveGeminiDispatchModel(tc.input)
			if got != tc.expected {
				t.Fatalf("TKResolveGeminiDispatchModel(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}
