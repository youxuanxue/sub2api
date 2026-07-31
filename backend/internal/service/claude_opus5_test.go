package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertClaudePricingMatchesRegistryOwner(t *testing.T, got *ModelPricing, owner string) {
	t.Helper()
	want := tkOverlayModelPricing(owner)
	require.NotNil(t, want, "missing registry owner %q", owner)
	require.NotNil(t, got)
	assert.InDelta(t, want.InputPricePerToken, got.InputPricePerToken, 1e-15)
	assert.InDelta(t, want.OutputPricePerToken, got.OutputPricePerToken, 1e-15)
	assert.InDelta(t, want.CacheCreationPricePerToken, got.CacheCreationPricePerToken, 1e-15)
	assert.InDelta(t, want.CacheReadPricePerToken, got.CacheReadPricePerToken, 1e-15)
}

// Opus 5 aliases must resolve the Opus 5 registry owner and never drift to an
// older Opus family row.
func TestClaudeOpus5_RegistryAliasUsesOpus5Owner(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)

	for _, model := range []string{"claude-opus-5", "us.anthropic.claude-opus-5-v1"} {
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err)
			require.NotNil(t, pricing)
			assertClaudePricingMatchesRegistryOwner(t, pricing, "claude-opus-5")
		})
	}
}

// Adjacent versions retain their own resolved registry owners; the test owns
// only alias relationships, never production price numbers.
func TestClaudeOpus5_AdjacentVersionsKeepResolvedOwners(t *testing.T) {
	svc := NewBillingService(&config.Config{}, nil)

	tests := []struct {
		model string
		owner string
	}{
		{"claude-opus-5", "claude-opus-5"},
		{"us.anthropic.claude-opus-5-v1", "claude-opus-5"},
		{"claude-opus-4-8", "claude-opus-4.8"},
		{"claude-opus-4-5-20251101", "claude-opus-4.5"},
		{"claude-opus-4-1-20250805", "claude-3-opus"},
		{"claude-3-opus-20240229", "claude-3-opus"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.NotNil(t, pricing)
			assertClaudePricingMatchesRegistryOwner(t, pricing, tt.owner)
		})
	}
}

// TestClaudeOpus5_BedrockCapabilityGates 锁定只有主版本号的模型 ID
// （claude-opus-5 / claude-sonnet-5）能被版本闸门识别。
// 修复前 claudeVersionRe 强制要求 major-minor，这类 ID 完全不匹配，
// 会被当成旧模型降级。
func TestClaudeOpus5_BedrockCapabilityGates(t *testing.T) {
	tests := []struct {
		modelID       string
		claude45Newer bool
		toolSearch    bool
		opus47Newer   bool
	}{
		{"claude-opus-5", true, true, true},
		{"us.anthropic.claude-opus-5-v1", true, true, true},
		{"eu.anthropic.claude-opus-5-v1", true, true, true},
		{"claude-sonnet-5", true, true, false},
		{"us.anthropic.claude-sonnet-5-v1", true, true, false},
		// 回归保护：旧模型不能因为 minor 可选而被误判为新版本
		{"anthropic.claude-opus-4-1-v1", false, false, false},
		{"anthropic.claude-sonnet-4-0-v1", false, false, false},
		{"anthropic.claude-3-opus-20240229-v1:0", false, false, false},
		{"us.anthropic.claude-opus-4-8-v1", true, true, true},
		// Haiku 不支持 tool search
		{"us.anthropic.claude-haiku-4-5-20251001-v1:0", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			assert.Equal(t, tt.claude45Newer, isBedrockClaude45OrNewer(tt.modelID), "isBedrockClaude45OrNewer")
			assert.Equal(t, tt.toolSearch, bedrockModelSupportsToolSearch(tt.modelID), "bedrockModelSupportsToolSearch")
			assert.Equal(t, tt.opus47Newer, isBedrockOpus47OrNewer(tt.modelID), "isBedrockOpus47OrNewer")
		})
	}
}

// TestClaudeOpus5_BedrockThinkingConvertedToAdaptive 验证 Opus 5 在 Bedrock 路径上
// 会把 thinking.type=enabled 转成 adaptive 并移除 budget_tokens。
// 上游 Opus 5 已移除 budget_tokens，透传过去会直接 400。
func TestClaudeOpus5_BedrockThinkingConvertedToAdaptive(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled","budget_tokens":10000}}`)
	got := sanitizeBedrockThinking(body, "us.anthropic.claude-opus-5-v1")

	assert.JSONEq(t, `{"thinking":{"type":"adaptive"}}`, string(got))
}

// TestClaudeOpus5_CatalogAndBedrockMapping 锁定模型清单与 Bedrock 默认映射。
func TestClaudeOpus5_CatalogAndBedrockMapping(t *testing.T) {
	assert.Contains(t, claude.DefaultModelIDs(), "claude-opus-5")

	mapped, ok := domain.DefaultBedrockModelMapping["claude-opus-5"]
	require.True(t, ok, "claude-opus-5 missing from DefaultBedrockModelMapping")
	assert.Equal(t, "us.anthropic.claude-opus-5-v1", mapped)
}
