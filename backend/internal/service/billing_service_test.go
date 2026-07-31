//go:build unit

package service

import (
	"bytes"
	"log"
	"math"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// captureStdLog 重定向 stdlib log 输出到 buffer,返回该 buffer;通过 t.Cleanup 还原。
// 用于断言 GetModelPricing 的 fallback warn(log.Printf)打了几次。
func captureStdLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

func newTestBillingService() *BillingService {
	return NewBillingService(&config.Config{}, nil)
}

func mustRegistryOwnerModelPricing(t *testing.T, model string) *ModelPricing {
	t.Helper()
	pricing := tkOverlayModelPricing(model)
	require.NotNil(t, pricing, "missing registry owner %q", model)
	return pricing
}

func requireModelPricingMatches(t *testing.T, want, got *ModelPricing) {
	t.Helper()
	require.NotNil(t, want)
	require.NotNil(t, got)
	require.InDelta(t, want.InputPricePerToken, got.InputPricePerToken, 1e-15)
	require.InDelta(t, want.InputPricePerTokenPriority, got.InputPricePerTokenPriority, 1e-15)
	require.InDelta(t, want.OutputPricePerToken, got.OutputPricePerToken, 1e-15)
	require.InDelta(t, want.OutputPricePerTokenPriority, got.OutputPricePerTokenPriority, 1e-15)
	require.InDelta(t, want.CacheCreationPricePerToken, got.CacheCreationPricePerToken, 1e-15)
	require.InDelta(t, want.CacheCreationPricePerTokenPriority, got.CacheCreationPricePerTokenPriority, 1e-15)
	require.InDelta(t, want.CacheReadPricePerToken, got.CacheReadPricePerToken, 1e-15)
	require.InDelta(t, want.CacheReadPricePerTokenPriority, got.CacheReadPricePerTokenPriority, 1e-15)
	require.InDelta(t, want.ImageInputPricePerToken, got.ImageInputPricePerToken, 1e-15)
	require.InDelta(t, want.ImageOutputPricePerToken, got.ImageOutputPricePerToken, 1e-15)
	require.Equal(t, want.LongContextInputThreshold, got.LongContextInputThreshold)
	require.InDelta(t, want.LongContextInputMultiplier, got.LongContextInputMultiplier, 1e-15)
	require.InDelta(t, want.LongContextOutputMultiplier, got.LongContextOutputMultiplier, 1e-15)
}

// newBillingServiceWithModelPricing builds an isolated registry fixture. Tests
// may inject prices, but the production BillingService has no second fallback
// map: every value still enters through PricingService's registry snapshot.
func newBillingServiceWithModelPricing(prices map[string]*ModelPricing) *BillingService {
	data := make(map[string]*LiteLLMModelPricing, len(prices))
	for model, price := range prices {
		if price == nil {
			continue
		}
		cacheCreation := price.CacheCreationPricePerToken
		if price.CacheCreation5mPrice > 0 {
			cacheCreation = price.CacheCreation5mPrice
		}
		data[model] = &LiteLLMModelPricing{
			InputCostPerToken:                   price.InputPricePerToken,
			InputCostPerTokenPriority:           price.InputPricePerTokenPriority,
			OutputCostPerToken:                  price.OutputPricePerToken,
			OutputCostPerTokenPriority:          price.OutputPricePerTokenPriority,
			CacheCreationInputTokenCost:         cacheCreation,
			CacheCreationInputTokenCostPriority: price.CacheCreationPricePerTokenPriority,
			CacheReadInputTokenCost:             price.CacheReadPricePerToken,
			CacheReadInputTokenCostPriority:     price.CacheReadPricePerTokenPriority,
			CacheCreationInputTokenCostAbove1hr: price.CacheCreation1hPrice,
			InputCostPerImageToken:              price.ImageInputPricePerToken,
			OutputCostPerImageToken:             price.ImageOutputPricePerToken,
			LongContextInputTokenThreshold:      price.LongContextInputThreshold,
			LongContextInputCostMultiplier:      price.LongContextInputMultiplier,
			LongContextOutputCostMultiplier:     price.LongContextOutputMultiplier,
			SupportsPromptCaching:               price.SupportsCacheBreakdown,
		}
	}
	return NewBillingService(&config.Config{}, &PricingService{pricingData: data})
}

func TestCalculateCost_BasicComputation(t *testing.T) {
	svc := newTestBillingService()
	owner := tkApplyOfficialListBaseTaxForModel(
		"claude-sonnet-4",
		mustRegistryOwnerModelPricing(t, "claude-sonnet-4"),
	)

	tokens := UsageTokens{
		InputTokens:  1000,
		OutputTokens: 500,
	}
	cost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	expectedInput := float64(tokens.InputTokens) * owner.InputPricePerToken
	expectedOutput := float64(tokens.OutputTokens) * owner.OutputPricePerToken
	require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.ActualCost, 1e-10)
}

func TestCalculateCost_GeminiProAgent_RegistryFamilyFloorNonZero(t *testing.T) {
	svc := newTestBillingService()
	cost, err := svc.CalculateCost("gemini-pro-agent", UsageTokens{InputTokens: 1000, OutputTokens: 500}, 1.0)
	require.NoError(t, err)
	require.Greater(t, cost.ActualCost, 0.0, "Wei-Shaw/sub2api#2486: gemini-pro-agent must never bill $0 via nil pricing")
}

func TestCalculateCost_WithCacheTokens(t *testing.T) {
	svc := newTestBillingService()
	owner := tkApplyOfficialListBaseTaxForModel(
		"claude-sonnet-4",
		mustRegistryOwnerModelPricing(t, "claude-sonnet-4"),
	)

	tokens := UsageTokens{
		InputTokens:         1000,
		OutputTokens:        500,
		CacheCreationTokens: 2000,
		CacheReadTokens:     3000,
	}
	cost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	expectedCacheCreation := float64(tokens.CacheCreationTokens) * owner.CacheCreationPricePerToken
	expectedCacheRead := float64(tokens.CacheReadTokens) * owner.CacheReadPricePerToken
	require.InDelta(t, expectedCacheCreation, cost.CacheCreationCost, 1e-10)
	require.InDelta(t, expectedCacheRead, cost.CacheReadCost, 1e-10)

	expectedTotal := cost.InputCost + cost.OutputCost + expectedCacheCreation + expectedCacheRead
	require.InDelta(t, expectedTotal, cost.TotalCost, 1e-10)
}

func TestCalculateCost_RateMultiplier(t *testing.T) {
	svc := newTestBillingService()

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}

	cost1x, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	cost2x, err := svc.CalculateCost("claude-sonnet-4", tokens, 2.0)
	require.NoError(t, err)

	// TotalCost 不受倍率影响，ActualCost 翻倍
	require.InDelta(t, cost1x.TotalCost, cost2x.TotalCost, 1e-10)
	require.InDelta(t, cost1x.ActualCost*2, cost2x.ActualCost, 1e-10)
}

func TestGetModelPricing_RegistryAliasesMatchResolvedOwners(t *testing.T) {
	svc := newTestBillingService()

	tests := []struct {
		model string
		owner string
	}{
		{"claude-opus-4.5-20250101", "claude-opus-4.5"},
		{"claude-3-opus-20240229", "claude-3-opus"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4"},
		{"claude-3-5-sonnet-20241022", "claude-3-5-sonnet"},
		{"claude-3-5-haiku-20241022", "claude-3-5-haiku"},
		{"claude-3-haiku-20240307", "claude-3-haiku"},
		{"claude-fable-5", "claude-fable-5"},
		{"claude-fable-5[1m]", "claude-fable-5"},
	}

	for _, tt := range tests {
		pricing, err := svc.GetModelPricing(tt.model)
		require.NoError(t, err, "模型 %s", tt.model)
		owner := mustRegistryOwnerModelPricing(t, tt.owner)
		requireModelPricingMatches(t, tkApplyOfficialListBaseTaxForModel(tt.model, owner), pricing)
	}
}

func TestGetModelPricing_CaseInsensitive(t *testing.T) {
	svc := newTestBillingService()

	p1, err := svc.GetModelPricing("Claude-Sonnet-4")
	require.NoError(t, err)

	p2, err := svc.GetModelPricing("claude-sonnet-4")
	require.NoError(t, err)

	require.Equal(t, p1.InputPricePerToken, p2.InputPricePerToken)
}

// issue #3394: registry alias warn 应按模型名去重,每个模型每进程最多打一条,
// 避免热路径每请求刷屏 ops_system_logs。
func TestGetModelPricing_RegistryAliasWarnLoggedOncePerModel(t *testing.T) {
	svc := newTestBillingService()
	buf := captureStdLog(t)

	// 使用没有直接 owner 的 Claude 变体，确保它走家族 alias 并触发 warn。
	const model = "claude-unknown-warning-a"
	for i := 0; i < 5; i++ {
		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err)
		require.NotNil(t, pricing)
	}

	got := strings.Count(buf.String(), "Using registry alias pricing for model: "+model)
	require.Equal(t, 1, got, "同一模型的 registry alias warn 应只打一条,实际日志:\n%s", buf.String())
}

// 去重按"每模型"而非全局:不同模型各打一条;大小写变体经入口 ToLower 归一,视为同一条目。
func TestGetModelPricing_RegistryAliasWarnPerModelNotGlobal(t *testing.T) {
	svc := newTestBillingService()
	buf := captureStdLog(t)

	for i := 0; i < 3; i++ {
		_, _ = svc.GetModelPricing("claude-unknown-warning-a")
		_, _ = svc.GetModelPricing("CLAUDE-UNKNOWN-WARNING-A") // ToLower 后视为同一条目
		_, _ = svc.GetModelPricing("claude-unknown-warning-b")
	}

	out := buf.String()
	require.Equal(t, 1, strings.Count(out, "model: claude-unknown-warning-a"), out)
	require.Equal(t, 1, strings.Count(out, "model: claude-unknown-warning-b"), out)
	require.Equal(t, 0, strings.Count(out, "model: CLAUDE-UNKNOWN-WARNING-A"), out)
}

// 回归：即使 PricingService 依赖未注入，glm-5.2 仍从 registry owner 精确恢复，
// 不会落到更宽的 glm-5 兼容分支。
func TestGetModelPricing_GLM52UsesBigModelPriceWithBaseTax(t *testing.T) {
	svc := newTestBillingService()

	got, err := svc.GetModelPricing("glm-5.2")
	require.NoError(t, err)
	require.NotNil(t, got)

	want := tkApplyOfficialListBaseTaxForModel("glm-5.2", mustRegistryOwnerModelPricing(t, "glm-5.2"))
	requireModelPricingMatches(t, want, got)
}

func TestGetModelPricing_UnknownClaudeModelFallsBackToSonnet(t *testing.T) {
	svc := newTestBillingService()

	// 不包含 opus/sonnet/haiku 关键词的 Claude 模型会解析到 Sonnet owner。
	pricing, err := svc.GetModelPricing("claude-unknown-model")
	require.NoError(t, err)
	owner := mustRegistryOwnerModelPricing(t, "claude-sonnet-4")
	requireModelPricingMatches(t, tkApplyOfficialListBaseTaxForModel("claude-unknown-model", owner), pricing)
}

func TestGetModelPricing_UnknownOpenAIModelReturnsError(t *testing.T) {
	svc := newTestBillingService()

	// gpt-* aliases to a registry family owner. A no-family-owner OpenAI id
	// (o-series, not "gpt"-named) remains fail-closed.
	pricing, err := svc.GetModelPricing("o5-unknown-preview")
	require.Error(t, err)
	require.Nil(t, pricing)
	require.Contains(t, err.Error(), "pricing not found")
}

func TestGetModelPricing_OpenAIGPT54UsesRegistryOwner(t *testing.T) {
	svc := newTestBillingService()

	pricing, err := svc.GetModelPricing("gpt-5.4")
	require.NoError(t, err)
	require.NotNil(t, pricing)
	requireModelPricingMatches(t, mustRegistryOwnerModelPricing(t, "gpt-5.4"), pricing)
}

func TestGetModelPricing_OpenAICompactAliasesResolveRegistryOwners(t *testing.T) {
	svc := newTestBillingService()

	tests := []struct {
		model string
		owner string
	}{
		{model: "gpt5.5", owner: "gpt-5.4"},
		{model: "openai/gpt5.4", owner: "gpt-5.4"},
		{model: "gpt5.4-mini", owner: "gpt-5.4-mini"},
		{model: "gpt5.3codexspark", owner: "gpt-5.3-codex"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(tt.model)
			require.NoError(t, err)
			require.NotNil(t, pricing)
			requireModelPricingMatches(t, mustRegistryOwnerModelPricing(t, tt.owner), pricing)
		})
	}
}

func TestGetModelPricing_OpenAIGPT54MiniUsesRegistryOwner(t *testing.T) {
	svc := newTestBillingService()

	pricing, err := svc.GetModelPricing("gpt-5.4-mini")
	require.NoError(t, err)
	require.NotNil(t, pricing)
	requireModelPricingMatches(t, mustRegistryOwnerModelPricing(t, "gpt-5.4-mini"), pricing)
}

func TestCalculateCost_OpenAIGPT54LongContextAppliesWholeSessionMultipliers(t *testing.T) {
	svc := newTestBillingService()
	owner := mustRegistryOwnerModelPricing(t, "gpt-5.4")

	tokens := UsageTokens{
		InputTokens:  owner.LongContextInputThreshold + 1000,
		OutputTokens: 4000,
	}

	cost, err := svc.CalculateCost("gpt-5.4-2026-03-05", tokens, 1.0)
	require.NoError(t, err)

	expectedInput := float64(tokens.InputTokens) * owner.InputPricePerToken * owner.LongContextInputMultiplier
	expectedOutput := float64(tokens.OutputTokens) * owner.OutputPricePerToken * owner.LongContextOutputMultiplier
	require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.ActualCost, 1e-10)
	require.True(t, cost.LongContextBillingApplied)
}

func TestCalculateCost_OpenAIGPT54LongContextMarkerRequiresActualCostIncrease(t *testing.T) {
	svc := newTestBillingService()
	owner := mustRegistryOwnerModelPricing(t, "gpt-5.4")

	cost, err := svc.calculateCostWithServiceTierPolicy(
		"gpt-5.4-2026-03-05",
		UsageTokens{InputTokens: owner.LongContextInputThreshold + 1000},
		0,
		"",
		true,
	)

	require.NoError(t, err)
	require.Zero(t, cost.ActualCost)
	require.False(t, cost.LongContextBillingApplied)
}

func TestCalculateCost_OpenAIGPT55ProUsesGPT55PricingPolicy(t *testing.T) {
	svc := newTestBillingService()
	owner := mustRegistryOwnerModelPricing(t, "gpt-5.5-pro")

	tokens := UsageTokens{
		InputTokens:  owner.LongContextInputThreshold + 1000,
		OutputTokens: 4000,
	}

	cost, err := svc.CalculateCost("gpt-5.5-pro", tokens, 1.0)
	require.NoError(t, err)

	expectedInput := float64(tokens.InputTokens) * owner.InputPricePerToken * owner.LongContextInputMultiplier
	expectedOutput := float64(tokens.OutputTokens) * owner.OutputPricePerToken * owner.LongContextOutputMultiplier
	require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedInput+expectedOutput, cost.ActualCost, 1e-10)
}

// 回归测试 #2293：长上下文计费触发时，cache_read_tokens 也应应用 registry owner 的 LongContextInputMultiplier。
func TestCalculateCost_OpenAIGPT54LongContextAppliesMultiplierToCacheRead(t *testing.T) {
	svc := newTestBillingService()
	owner := mustRegistryOwnerModelPricing(t, "gpt-5.4")

	tokens := UsageTokens{
		InputTokens:     1000,
		CacheReadTokens: owner.LongContextInputThreshold,
		OutputTokens:    1000,
	}

	cost, err := svc.CalculateCost("gpt-5.4-2026-03-05", tokens, 1.0)
	require.NoError(t, err)

	expectedInput := float64(tokens.InputTokens) * owner.InputPricePerToken * owner.LongContextInputMultiplier
	expectedOutput := float64(tokens.OutputTokens) * owner.OutputPricePerToken * owner.LongContextOutputMultiplier
	expectedCacheRead := float64(tokens.CacheReadTokens) * owner.CacheReadPricePerToken * owner.LongContextInputMultiplier

	require.InDelta(t, expectedInput, cost.InputCost, 1e-10)
	require.InDelta(t, expectedOutput, cost.OutputCost, 1e-10)
	require.InDelta(t, expectedCacheRead, cost.CacheReadCost, 1e-10,
		"cache_read_cost should be scaled by LongContextInputMultiplier when long-context pricing applies (issue #2293)")

	expectedTotal := expectedInput + expectedOutput + expectedCacheRead
	require.InDelta(t, expectedTotal, cost.TotalCost, 1e-10)
	require.InDelta(t, expectedTotal, cost.ActualCost, 1e-10)
}

// 阴性测试：未触发长上下文时，cache_read_price 不应被错误地乘以倍率。
func TestCalculateCost_OpenAIGPT54NoLongContextKeepsCacheReadAtBasePrice(t *testing.T) {
	svc := newTestBillingService()
	owner := mustRegistryOwnerModelPricing(t, "gpt-5.4")

	tokens := UsageTokens{
		InputTokens:     1000,
		CacheReadTokens: owner.LongContextInputThreshold / 2,
		OutputTokens:    1000,
	}

	cost, err := svc.CalculateCost("gpt-5.4-2026-03-05", tokens, 1.0)
	require.NoError(t, err)

	expectedCacheRead := float64(tokens.CacheReadTokens) * owner.CacheReadPricePerToken
	require.InDelta(t, expectedCacheRead, cost.CacheReadCost, 1e-10,
		"cache_read_cost should remain at base price when below long-context threshold")
}

// 回归测试 #2816 follow-up：长上下文计费触发时，cache_creation_tokens 也应应用
// LongContextInputMultiplier。computeCacheCreationCost 直接读取 pricing.* 价格，
// 不经过 computeTokenBreakdown 内的 inputPrice / cacheReadPrice 倍率修改，因此
// 修复前 cache_creation 部分会按基础价计算，少计费用约 50%（默认倍率 2.0）。
func TestCalculateCost_OpenAIGPT54LongContextAppliesMultiplierToCacheCreation(t *testing.T) {
	svc := newTestBillingService()
	owner := mustRegistryOwnerModelPricing(t, "gpt-5.4")

	tokens := UsageTokens{
		InputTokens:         1000,
		CacheReadTokens:     owner.LongContextInputThreshold,
		CacheCreationTokens: 10000,
		OutputTokens:        1000,
	}

	cost, err := svc.CalculateCost("gpt-5.4-2026-03-05", tokens, 1.0)
	require.NoError(t, err)

	expectedCacheCreation := float64(tokens.CacheCreationTokens) * owner.CacheCreationPricePerToken * owner.LongContextInputMultiplier
	require.InDelta(t, expectedCacheCreation, cost.CacheCreationCost, 1e-10,
		"cache_creation_cost should be scaled by LongContextInputMultiplier when long-context pricing applies")
}

// 阴性测试：未触发长上下文时，cache_creation_price 不应被错误地乘以倍率。
func TestCalculateCost_OpenAIGPT54NoLongContextKeepsCacheCreationAtBasePrice(t *testing.T) {
	svc := newTestBillingService()
	owner := mustRegistryOwnerModelPricing(t, "gpt-5.4")

	tokens := UsageTokens{
		InputTokens:         1000,
		CacheReadTokens:     owner.LongContextInputThreshold / 2,
		CacheCreationTokens: 10000,
		OutputTokens:        1000,
	}

	cost, err := svc.CalculateCost("gpt-5.4-2026-03-05", tokens, 1.0)
	require.NoError(t, err)

	expectedCacheCreation := float64(tokens.CacheCreationTokens) * owner.CacheCreationPricePerToken
	require.InDelta(t, expectedCacheCreation, cost.CacheCreationCost, 1e-10,
		"cache_creation_cost should remain at base price when below long-context threshold")
}

// 覆盖 5m / 1h ephemeral 分类计费路径：长上下文触发时两档价格都应被倍率缩放。
// 使用手工构造的 pricing（参考 TestCalculateCost_SupportsCacheBreakdown 的写法）
// 以便同时控制 SupportsCacheBreakdown + 长上下文阈值。
func TestCalculateCost_LongContextAppliesMultiplierToCacheCreation5mAnd1h(t *testing.T) {
	svc := newBillingServiceWithModelPricing(map[string]*ModelPricing{
		"fixture-long-context-model": {
			InputPricePerToken:          3e-6,
			OutputPricePerToken:         15e-6,
			CacheReadPricePerToken:      0.3e-6,
			SupportsCacheBreakdown:      true,
			CacheCreation5mPrice:        4e-6,
			CacheCreation1hPrice:        5e-6,
			LongContextInputThreshold:   272000,
			LongContextInputMultiplier:  2.0,
			LongContextOutputMultiplier: 1.5,
		},
	})

	// InputTokens + CacheReadTokens = 1000 + 300000 = 301000 > 272000 阈值
	tokens := UsageTokens{
		InputTokens:           1000,
		CacheReadTokens:       300000,
		CacheCreation5mTokens: 8000,
		CacheCreation1hTokens: 4000,
		OutputTokens:          1000,
	}

	cost, err := svc.CalculateCost("fixture-long-context-model", tokens, 1.0)
	require.NoError(t, err)

	expected5m := float64(tokens.CacheCreation5mTokens) * 4e-6 * 2.0
	expected1h := float64(tokens.CacheCreation1hTokens) * 5e-6 * 2.0
	require.InDelta(t, expected5m+expected1h, cost.CacheCreationCost, 1e-10,
		"both 5m and 1h cache_creation prices should be scaled by LongContextInputMultiplier")
}

func TestGetRegistryAliasPricing_FamilyMatching(t *testing.T) {
	svc := newTestBillingService()

	// This table owns only compatibility relationships. All price dimensions are
	// derived from the resolved registry owner so a price edit has one edit site.
	tests := []struct {
		name  string
		model string
		owner string
	}{
		{name: "empty model", model: "   "},
		{name: "claude opus 4.6", model: "claude-opus-4.6-20260201", owner: "claude-opus-4.6"},
		{name: "claude opus 4.5 alt separator", model: "claude-opus-4-5-20260101", owner: "claude-opus-4.5"},
		{name: "claude generic model uses sonnet owner", model: "claude-foo-bar", owner: "claude-sonnet-4"},
		{name: "claude fable 5", model: "claude-fable-5", owner: "claude-fable-5"},
		{name: "claude fable 5 1m alias", model: "claude-fable-5[1m]", owner: "claude-fable-5"},

		{name: "gemini pro family floor", model: "gemini-2.0-pro", owner: "gemini-2.5-pro"},
		{name: "gemini pro agent family floor", model: "gemini-pro-agent", owner: "gemini-2.5-pro"},
		{name: "gemini flash family floor", model: "gemini-9-flash-preview", owner: "gemini-2.5-flash"},
		{name: "gemini flash lite family floor", model: "gemini-9-flash-lite-x", owner: "gemini-2.5-flash-lite"},
		{name: "gemini unknown uses flash family floor", model: "gemini-9-ultra", owner: "gemini-2.5-flash"},

		{name: "openai gpt5.4", model: "gpt-5.4", owner: "gpt-5.4"},
		{name: "openai gpt5.4 mini", model: "gpt-5.4-mini", owner: "gpt-5.4-mini"},
		{name: "openai gpt-5.6 sol", model: "gpt-5.6-sol", owner: "gpt-5.6-sol"},
		{name: "openai gpt-5.6 terra", model: "gpt-5.6-terra", owner: "gpt-5.6-terra"},
		{name: "openai gpt-5.6 luna", model: "gpt-5.6-luna", owner: "gpt-5.6-luna"},
		{name: "openai gpt-5.6 chat-latest uses sol", model: "gpt-5.6-chat-latest", owner: "gpt-5.6-sol"},
		{name: "openai gpt5.3 codex", model: "gpt-5.3-codex", owner: "gpt-5.3-codex"},
		{name: "openai gpt5.3 codex spark", model: "gpt-5.3-codex-spark", owner: "gpt-5.3-codex"},
		{name: "openai legacy gpt5.1 uses gpt5.4", model: "gpt-5.1", owner: "gpt-5.4"},
		{name: "openai legacy gpt5.1 codex uses gpt5.3 codex", model: "gpt-5.1-codex", owner: "gpt-5.3-codex"},
		{name: "openai legacy codex mini latest uses gpt5.3 codex", model: "codex-mini-latest", owner: "gpt-5.3-codex"},
		{name: "unknown gpt chat uses gpt5.4 family floor", model: "gpt-unknown-model", owner: "gpt-5.4"},
		{name: "openai o-series unknown", model: "o5-preview"},
		{name: "gpt image excluded from chat floor", model: "gpt-image-2-unknown"},
		{name: "gpt audio excluded from chat floor", model: "gpt-audio-x-unknown"},
		{name: "gpt realtime excluded from chat floor", model: "gpt-realtime-x-unknown"},

		{name: "deepseek v4 pro", model: "deepseek-v4-pro", owner: "deepseek-v4-pro"},
		{name: "deepseek v4 flash", model: "deepseek-v4-flash", owner: "deepseek-v4-flash"},
		{name: "deepseek chat alias", model: "deepseek-chat", owner: "deepseek-v4-flash"},
		{name: "deepseek reasoner alias", model: "deepseek-reasoner", owner: "deepseek-v4-flash"},

		{name: "glm 5.2 flagship", model: "glm-5.2", owner: "glm-5.2"},
		{name: "glm 5.1 flagship", model: "glm-5.1", owner: "glm-5.1"},
		{name: "glm 5 base", model: "glm-5", owner: "glm-5"},
		{name: "glm 5 turbo", model: "glm-5-turbo", owner: "glm-5-turbo"},
		{name: "glm 4.7", model: "glm-4.7", owner: "glm-4.7"},
		{name: "glm 4.6", model: "glm-4.6", owner: "glm-4.6"},
		{name: "glm 4.5", model: "glm-4.5", owner: "glm-4.5"},
		{name: "glm 4.5 air", model: "glm-4.5-air", owner: "glm-4.5-air"},
		{name: "glm 4.7 flashx", model: "glm-4.7-flashx", owner: "glm-4.7-flashx"},
		{name: "glm 4.5 flash free", model: "glm-4.5-flash", owner: "glm-4.5-flash"},
		{name: "glm 4.7 flash free", model: "glm-4.7-flash", owner: "glm-4.7-flash"},
		{name: "glm 4.5 x absent", model: "glm-4.5-x"},
		{name: "glm 4.5 airx absent", model: "glm-4.5-airx"},
		{name: "glm 5.2 ordering", model: "glm-5.2", owner: "glm-5.2"},
		{name: "glm 5.1 ordering", model: "glm-5.1", owner: "glm-5.1"},
		{name: "glm 4.5 air ordering", model: "glm-4.5-air", owner: "glm-4.5-air"},

		{name: "kimi k3 flagship", model: "kimi-k3", owner: "kimi-k3"},
		{name: "kimi code bare k3", model: "k3", owner: "kimi-k3"},
		{name: "kimi code bare k3 256k", model: "k3-256k", owner: "kimi-k3"},
		{name: "kimi k3 path suffix", model: "moonshot/kimi-k3", owner: "kimi-k3"},
		{name: "kimi code path suffix", model: "kimi-code/k3", owner: "kimi-k3"},
		{name: "kimi k2.6 flagship", model: "kimi-k2.6", owner: "kimi-k2.6"},
		{name: "kimi for coding", model: "kimi-for-coding", owner: "kimi-k2.6"},
		{name: "kimi k2.5", model: "kimi-k2.5", owner: "kimi-k2.5"},
		{name: "kimi k2 thinking", model: "kimi-k2-thinking", owner: "kimi-k2-thinking"},
		{name: "kimi k2 base", model: "kimi-k2", owner: "kimi-k2"},
		{name: "kimi k2.6 ordering", model: "kimi-k2.6", owner: "kimi-k2.6"},
		{name: "kimi k2 thinking hyphenated", model: "kimi-k2-thinking-preview", owner: "kimi-k2-thinking"},
		{name: "kimi k2 dated compatibility alias", model: "kimi-k2-0905-preview", owner: "kimi-k2"},

		{name: "minimax m3", model: "minimax-m3", owner: "minimax-m3"},
		{name: "minimax m3 suffix", model: "minimax-m3-long", owner: "minimax-m3"},
		{name: "minimax m2.7", model: "minimax-m2.7", owner: "minimax-m2.7"},
		{name: "minimax m2.7 highspeed", model: "minimax-m2.7-highspeed", owner: "minimax-m2.7-highspeed"},
		{name: "minimax m2.5", model: "minimax-m2.5", owner: "minimax-m2.5"},
		{name: "minimax m2 legacy", model: "minimax-m2", owner: "minimax-m2"},

		{name: "doubao embedding vision", model: "doubao-embedding-vision", owner: "doubao-embedding-vision"},
		{name: "doubao embedding vision versioned", model: "doubao-embedding-vision-251215", owner: "doubao-embedding-vision"},

		{name: "qwen unknown", model: "qwen-max"},
		{name: "doubao unknown", model: "doubao-pro"},
		{name: "doubao text embedding unknown", model: "doubao-embedding-text-240515"},
		{name: "hunyuan unknown", model: "hunyuan-t1"},
		{name: "moonshot v1 not covered", model: "moonshot-v1-8k"},
		{name: "k3-like unknown", model: "foo-k3-bar"},
		{name: "path segment not bare k3", model: "vendor/foo-k3"},
		{name: "kimi k30 unknown", model: "kimi-k30"},
		{name: "embedded kimi k3 unknown", model: "foo-kimi-k3-bar"},
		{name: "kimi k3 1m is not an API alias", model: "kimi-k3[1m]"},
		{name: "path kimi k3 1m is not an API alias", model: "moonshot/kimi-k3[1m]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svc.getRegistryAliasPricing(tt.model)
			if tt.owner == "" {
				require.Nil(t, got)
				return
			}
			want := mustRegistryOwnerModelPricing(t, tt.owner)
			requireModelPricingMatches(t, want, got)
		})
	}
}

// doubao-embedding-vision 是首个图文不同价的 embedding；验证 owner 的两档
// 单价能被版本后缀和大小写别名一致解析。
func TestGetModelPricing_DoubaoEmbeddingVisionImageInputRate(t *testing.T) {
	svc := newTestBillingService()
	want := tkApplyOfficialListBaseTaxForModel(
		"doubao-embedding-vision",
		mustRegistryOwnerModelPricing(t, "doubao-embedding-vision"),
	)

	for _, model := range []string{
		"doubao-embedding-vision",
		"doubao-embedding-vision-251215",
		"Doubao-Embedding-Vision",
	} {
		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err, "model %s should resolve registry pricing", model)
		requireModelPricingMatches(t, want, pricing)
		require.Zero(t, pricing.OutputPricePerToken, "embedding has no output cost for %s", model)
	}
}

// 验证双档计费：InputCost = 文本token×文本价 + 图片token×图片价；
// 且 ImageInputTokens=0 时走原单价路径，ImageInputTokens>InputTokens 时不负计文本。
func TestCalculateCost_DoubaoEmbeddingVisionDifferentialInput(t *testing.T) {
	svc := newTestBillingService()
	pricing, err := svc.GetModelPricing("doubao-embedding-vision")
	require.NoError(t, err)

	// 图文混合：prompt_tokens=1340，其中 image_tokens=28、text_tokens=1312。
	mixed := UsageTokens{InputTokens: 1340, ImageInputTokens: 28}
	cost, err := svc.CalculateCost("doubao-embedding-vision", mixed, 1.0)
	require.NoError(t, err)
	textRate := pricing.InputPricePerToken
	imageRate := pricing.ImageInputPricePerToken
	wantMixed := float64(1312)*textRate + float64(28)*imageRate
	require.InDelta(t, wantMixed, cost.InputCost, 1e-15)
	require.InDelta(t, wantMixed, cost.TotalCost, 1e-15)
	require.Zero(t, cost.OutputCost)

	// 纯文本：全部按文本档计费，与原单价路径一致。
	textOnly := UsageTokens{InputTokens: 1340}
	costText, err := svc.CalculateCost("doubao-embedding-vision", textOnly, 1.0)
	require.NoError(t, err)
	require.InDelta(t, float64(1340)*textRate, costText.InputCost, 1e-15)

	// 健壮性：ImageInputTokens 超过 InputTokens 时，文本置 0、计费 token 不超过 InputTokens。
	weird := UsageTokens{InputTokens: 10, ImageInputTokens: 50}
	costWeird, err := svc.CalculateCost("doubao-embedding-vision", weird, 1.0)
	require.NoError(t, err)
	require.InDelta(t, float64(10)*imageRate, costWeird.InputCost, 1e-15)
}
func TestCalculateCostWithLongContext_BelowThreshold(t *testing.T) {
	svc := newTestBillingService()

	tokens := UsageTokens{
		InputTokens:     50000,
		OutputTokens:    1000,
		CacheReadTokens: 100000,
	}
	// 总输入 150k < 200k 阈值，应走正常计费
	cost, err := svc.CalculateCostWithLongContext("claude-sonnet-4", tokens, 1.0, 200000, 2.0)
	require.NoError(t, err)

	normalCost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	require.InDelta(t, normalCost.ActualCost, cost.ActualCost, 1e-10)
}

func TestCalculateCostWithLongContext_AboveThreshold_CacheExceedsThreshold(t *testing.T) {
	svc := newTestBillingService()

	// 缓存 210k + 输入 10k = 220k > 200k 阈值
	// 缓存已超阈值：范围内 200k 缓存，范围外 10k 缓存 + 10k 输入
	tokens := UsageTokens{
		InputTokens:     10000,
		OutputTokens:    1000,
		CacheReadTokens: 210000,
	}
	cost, err := svc.CalculateCostWithLongContext("claude-sonnet-4", tokens, 1.0, 200000, 2.0)
	require.NoError(t, err)

	// 范围内：200k cache + 0 input + 1k output
	inRange, _ := svc.CalculateCost("claude-sonnet-4", UsageTokens{
		InputTokens:     0,
		OutputTokens:    1000,
		CacheReadTokens: 200000,
	}, 1.0)

	// 范围外：10k cache + 10k input，倍率 2.0
	outRange, _ := svc.CalculateCost("claude-sonnet-4", UsageTokens{
		InputTokens:     10000,
		CacheReadTokens: 10000,
	}, 2.0)

	require.InDelta(t, inRange.ActualCost+outRange.ActualCost, cost.ActualCost, 1e-10)
}

func TestCalculateCostWithLongContext_AboveThreshold_CacheBelowThreshold(t *testing.T) {
	svc := newTestBillingService()

	// 缓存 100k + 输入 150k = 250k > 200k 阈值
	// 缓存未超阈值：范围内 100k 缓存 + 100k 输入，范围外 50k 输入
	tokens := UsageTokens{
		InputTokens:     150000,
		OutputTokens:    1000,
		CacheReadTokens: 100000,
	}
	cost, err := svc.CalculateCostWithLongContext("claude-sonnet-4", tokens, 1.0, 200000, 2.0)
	require.NoError(t, err)

	require.True(t, cost.ActualCost > 0, "费用应大于 0")

	// 正常费用不含长上下文
	normalCost, _ := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.True(t, cost.ActualCost > normalCost.ActualCost, "长上下文费用应高于正常费用")
}

func TestCalculateCostWithLongContext_MarkerRequiresActualCostIncrease(t *testing.T) {
	svc := newTestBillingService()
	tokens := UsageTokens{InputTokens: 300000}

	cost, err := svc.CalculateCostWithLongContext("claude-sonnet-4", tokens, 0, 200000, 2.0)

	require.NoError(t, err)
	require.Zero(t, cost.ActualCost)
	require.False(t, cost.LongContextBillingApplied)
}

func TestCalculateCostWithLongContext_DisabledThreshold(t *testing.T) {
	svc := newTestBillingService()

	tokens := UsageTokens{InputTokens: 300000, CacheReadTokens: 0}

	// threshold <= 0 应禁用长上下文计费
	cost1, err := svc.CalculateCostWithLongContext("claude-sonnet-4", tokens, 1.0, 0, 2.0)
	require.NoError(t, err)

	cost2, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	require.InDelta(t, cost2.ActualCost, cost1.ActualCost, 1e-10)
}

func TestCalculateCostWithLongContext_ExtraMultiplierLessEqualOne(t *testing.T) {
	svc := newTestBillingService()

	tokens := UsageTokens{InputTokens: 300000}

	// extraMultiplier <= 1 应禁用长上下文计费
	cost, err := svc.CalculateCostWithLongContext("claude-sonnet-4", tokens, 1.0, 200000, 1.0)
	require.NoError(t, err)

	normalCost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	require.InDelta(t, normalCost.ActualCost, cost.ActualCost, 1e-10)
}

func TestCalculateImageCost(t *testing.T) {
	svc := newTestBillingService()

	price := 0.123
	cfg := &ImagePriceConfig{Price1K: &price}
	cost := svc.CalculateImageCost("gpt-image-1", "1K", 3, cfg, 1.0)

	require.InDelta(t, price*3, cost.TotalCost, 1e-10)
	require.InDelta(t, price*3, cost.ActualCost, 1e-10)
}

func TestCalculateVideoCostUsesSeparateConfig(t *testing.T) {
	svc := newTestBillingService()

	imagePrice := 0.4
	videoPrice := 0.08
	imageCost := svc.CalculateImageCost("grok-imagine-video", "2K", 1, &ImagePriceConfig{Price2K: &imagePrice}, 1.0)
	videoCost := svc.CalculateVideoCost("grok-imagine-video", "480p", 1, 10, &VideoPriceConfig{Price480P: &videoPrice}, 0.5, nil)

	require.InDelta(t, 0.4, imageCost.TotalCost, 1e-10)
	require.InDelta(t, 0.8, videoCost.TotalCost, 1e-10)
	require.InDelta(t, 0.4, videoCost.ActualCost, 1e-10)
	require.Equal(t, string(BillingModeVideo), videoCost.BillingMode)
}

func TestCalculateVideoCostBillsPerSecond(t *testing.T) {
	svc := newTestBillingService()
	unit, ok := tkOverlayVideoUnitPriceUSD("grok-imagine-video", VideoBillingResolution720P, nil)
	require.True(t, ok, "registry must define grok-imagine-video 720p")

	oneSecond := svc.CalculateVideoCost("grok-imagine-video", "720p", 1, 1, nil, 1.0, nil)
	fifteenSeconds := svc.CalculateVideoCost("grok-imagine-video", "720p", 1, 15, nil, 1.0, nil)
	// duration <=0 时按上游默认 8 秒计费，超出上限按 15 秒收敛。
	defaultDuration := svc.CalculateVideoCost("grok-imagine-video", "720p", 1, 0, nil, 1.0, nil)
	clampedDuration := svc.CalculateVideoCost("grok-imagine-video", "720p", 1, 999, nil, 1.0, nil)

	require.InDelta(t, unit, oneSecond.TotalCost, 1e-10)
	require.InDelta(t, unit*15, fifteenSeconds.TotalCost, 1e-10)
	require.InDelta(t, unit*8, defaultDuration.TotalCost, 1e-10)
	require.InDelta(t, unit*15, clampedDuration.TotalCost, 1e-10)
}

func TestCalculateGrokImagineImageCostUsesDefaultRateCard(t *testing.T) {
	svc := newTestBillingService()
	standardOwner := tkOverlayLiteLLMModelPricing("grok-imagine-image")
	qualityOwner := tkOverlayLiteLLMModelPricing("grok-imagine-image-quality")
	require.NotNil(t, standardOwner)
	require.NotNil(t, qualityOwner)

	standard1K := svc.CalculateImageCost("grok-imagine-image", "1K", 1, nil, 1.0)
	standard2K := svc.CalculateImageCost("grok-imagine-image", "2K", 1, nil, 1.0)
	quality1K := svc.CalculateImageCost("grok-imagine-image-quality", "1K", 1, nil, 1.0)
	quality2K := svc.CalculateImageCost("grok-imagine-image-quality", "2K", 1, nil, 1.0)

	require.InDelta(t, standardOwner.ImagePrice1K, standard1K.TotalCost, 1e-12)
	require.InDelta(t, standardOwner.ImagePrice2K, standard2K.TotalCost, 1e-12)
	require.InDelta(t, qualityOwner.ImagePrice1K, quality1K.TotalCost, 1e-12)
	require.InDelta(t, qualityOwner.ImagePrice2K, quality2K.TotalCost, 1e-12)
}

func TestCalculateGrokImagineVideoCostUsesRegistryTierCard(t *testing.T) {
	svc := newTestBillingService()

	cases := []struct {
		model      string
		resolution string
	}{
		{"grok-imagine-video", "480p"},
		{"grok-imagine-video", "720p"},
		{"grok-imagine-video-1.5", "480p"},
		{"grok-imagine-video-1.5", "720p"},
		{"grok-imagine-video-1.5", "1080p"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.model+"@"+tc.resolution, func(t *testing.T) {
			want, ok := tkOverlayVideoUnitPriceUSD(tc.model, tc.resolution, nil)
			require.True(t, ok)
			got := svc.CalculateVideoCost(tc.model, tc.resolution, 1, 1, nil, 1.0, nil)
			require.InDelta(t, want, got.TotalCost, 1e-10)
		})
	}
}

func TestIsModelSupported(t *testing.T) {
	svc := newTestBillingService()

	require.True(t, svc.IsModelSupported("claude-sonnet-4"))
	require.True(t, svc.IsModelSupported("Claude-Opus-4.5"))
	require.True(t, svc.IsModelSupported("claude-3-haiku"))
	require.False(t, svc.IsModelSupported("gpt-4o"))
	require.False(t, svc.IsModelSupported("gemini-pro"))
}

func TestCalculateCost_ZeroTokens(t *testing.T) {
	svc := newTestBillingService()

	cost, err := svc.CalculateCost("claude-sonnet-4", UsageTokens{}, 1.0)
	require.NoError(t, err)
	require.Equal(t, 0.0, cost.TotalCost)
	require.Equal(t, 0.0, cost.ActualCost)
}

func TestCalculateCostWithConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1.5
	svc := NewBillingService(cfg, nil)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}
	cost, err := svc.CalculateCostWithConfig("claude-sonnet-4", tokens)
	require.NoError(t, err)

	expected, _ := svc.CalculateCost("claude-sonnet-4", tokens, 1.5)
	require.InDelta(t, expected.ActualCost, cost.ActualCost, 1e-10)
}

func TestCalculateCostWithConfig_ZeroMultiplier(t *testing.T) {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 0
	svc := NewBillingService(cfg, nil)

	tokens := UsageTokens{InputTokens: 1000}
	cost, err := svc.CalculateCostWithConfig("claude-sonnet-4", tokens)
	require.NoError(t, err)

	// 倍率 <=0 时默认 1.0
	expected, _ := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.InDelta(t, expected.ActualCost, cost.ActualCost, 1e-10)
}

func TestGetEstimatedCost(t *testing.T) {
	svc := newTestBillingService()

	est, err := svc.GetEstimatedCost("claude-sonnet-4", 1000, 500)
	require.NoError(t, err)
	require.True(t, est > 0)
}

func TestListSupportedModels(t *testing.T) {
	svc := newTestBillingService()

	models := svc.ListSupportedModels()
	require.NotEmpty(t, models)
	require.GreaterOrEqual(t, len(models), 6)
}

func TestCalculateCostWithLongContext_PropagatesError(t *testing.T) {
	// An empty registry fixture makes GetModelPricing fail closed.
	svc := newBillingServiceWithModelPricing(nil)

	tokens := UsageTokens{InputTokens: 300000, CacheReadTokens: 0}
	_, err := svc.CalculateCostWithLongContext("unknown-model", tokens, 1.0, 200000, 2.0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "pricing not found")
}

func TestGetModelPricing_Grok45AliasesUseRegistryOwner(t *testing.T) {
	svc := newTestBillingService()
	want := mustRegistryOwnerModelPricing(t, "grok-4.5")

	for _, model := range []string{"grok", "grok-latest", "grok-4.5", "grok-4.5-latest", "grok-build-latest"} {
		model := model
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err)
			requireModelPricingMatches(t, want, pricing)
		})
	}
}

func TestGetModelPricing_GrokCatalogAliasesUseRegistryOwners(t *testing.T) {
	svc := newTestBillingService()

	tests := []struct {
		name   string
		owner  string
		models []string
	}{
		{
			name:  "Grok 4.3 family",
			owner: "grok-4.3",
			models: []string{
				"grok-4.3",
				"grok-4.20-0309-reasoning",
				"grok-4.20-0309-non-reasoning",
				"grok-4.20-multi-agent-0309",
				"grok-4.20-reasoning",
				"grok-4.20-non-reasoning",
			},
		},
		{
			name:  "Grok coding and Composer family",
			owner: "grok-build-0.1",
			models: []string{
				"grok-build",
				"grok-build-0.1",
				"grok-composer",
				"grok-composer-2.5-fast",
				"composer-2.5",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := mustRegistryOwnerModelPricing(t, tt.owner)
			for _, model := range tt.models {
				pricing, err := svc.GetModelPricing(model)
				require.NoError(t, err, "model %s", model)
				requireModelPricingMatches(t, want, pricing)
			}
		})
	}
}

func TestCalculateCost_SupportsCacheBreakdown(t *testing.T) {
	const model = "fixture-cache-breakdown"
	svc := newBillingServiceWithModelPricing(map[string]*ModelPricing{
		model: {
			InputPricePerToken:     3e-6,
			OutputPricePerToken:    15e-6,
			SupportsCacheBreakdown: true,
			CacheCreation5mPrice:   4e-6, // per token
			CacheCreation1hPrice:   5e-6, // per token
		},
	})

	tokens := UsageTokens{
		InputTokens:           1000,
		OutputTokens:          500,
		CacheCreation5mTokens: 100000,
		CacheCreation1hTokens: 50000,
	}
	cost, err := svc.CalculateCost(model, tokens, 1.0)
	require.NoError(t, err)

	expected5m := float64(tokens.CacheCreation5mTokens) * 4e-6
	expected1h := float64(tokens.CacheCreation1hTokens) * 5e-6
	require.InDelta(t, expected5m+expected1h, cost.CacheCreationCost, 1e-10)
}

func TestCalculateCost_LargeTokenCount(t *testing.T) {
	svc := newTestBillingService()
	owner := tkApplyOfficialListBaseTaxForModel(
		"claude-sonnet-4",
		mustRegistryOwnerModelPricing(t, "claude-sonnet-4"),
	)

	tokens := UsageTokens{
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
	}
	cost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	require.InDelta(t, float64(tokens.InputTokens)*owner.InputPricePerToken, cost.InputCost, 1e-6)
	require.InDelta(t, float64(tokens.OutputTokens)*owner.OutputPricePerToken, cost.OutputCost, 1e-6)
	require.False(t, math.IsNaN(cost.TotalCost))
	require.False(t, math.IsInf(cost.TotalCost, 0))
}

func TestServiceTierCostMultiplier(t *testing.T) {
	require.InDelta(t, 2.0, serviceTierCostMultiplier("priority"), 1e-12)
	require.InDelta(t, 2.0, serviceTierCostMultiplier(" Priority "), 1e-12)
	require.InDelta(t, 0.5, serviceTierCostMultiplier("flex"), 1e-12)
	require.InDelta(t, 1.0, serviceTierCostMultiplier(""), 1e-12)
	require.InDelta(t, 1.0, serviceTierCostMultiplier("default"), 1e-12)
}

func TestCalculateCostWithServiceTier_OpenAIPriorityUsesPriorityPricing(t *testing.T) {
	svc := newTestBillingService()
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 20}

	baseCost, err := svc.CalculateCost("gpt-5.1-codex", tokens, 1.0)
	require.NoError(t, err)

	priorityCost, err := svc.CalculateCostWithServiceTier("gpt-5.1-codex", tokens, 1.0, "priority")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*2, priorityCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*2, priorityCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*2, priorityCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*2, priorityCost.TotalCost, 1e-10)
}

func TestCalculateCostWithServiceTier_FlexAppliesHalfMultiplier(t *testing.T) {
	svc := newTestBillingService()
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 40, CacheReadTokens: 20}

	baseCost, err := svc.CalculateCost("gpt-5.4", tokens, 1.0)
	require.NoError(t, err)

	flexCost, err := svc.CalculateCostWithServiceTier("gpt-5.4", tokens, 1.0, "flex")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*0.5, flexCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*0.5, flexCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*0.5, flexCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*0.5, flexCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*0.5, flexCost.TotalCost, 1e-10)
}

func TestCalculateCostWithServiceTier_Gpt54MiniPriorityFallsBackToTierMultiplier(t *testing.T) {
	svc := newTestBillingService()
	tokens := UsageTokens{InputTokens: 120, OutputTokens: 30, CacheCreationTokens: 12, CacheReadTokens: 8}

	baseCost, err := svc.CalculateCost("gpt-5.4-mini", tokens, 1.0)
	require.NoError(t, err)

	priorityCost, err := svc.CalculateCostWithServiceTier("gpt-5.4-mini", tokens, 1.0, "priority")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*2, priorityCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*2, priorityCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*2, priorityCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*2, priorityCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*2, priorityCost.TotalCost, 1e-10)
}

func TestCalculateCostWithServiceTier_Gpt54NanoFlexAppliesHalfMultiplier(t *testing.T) {
	svc := newTestBillingService()
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 40, CacheReadTokens: 20}

	baseCost, err := svc.CalculateCost("gpt-5.4-nano", tokens, 1.0)
	require.NoError(t, err)

	flexCost, err := svc.CalculateCostWithServiceTier("gpt-5.4-nano", tokens, 1.0, "flex")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*0.5, flexCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*0.5, flexCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*0.5, flexCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*0.5, flexCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*0.5, flexCost.TotalCost, 1e-10)
}

func TestCalculateCostWithServiceTier_PriorityFallsBackToTierMultiplierWithoutExplicitPriorityPrice(t *testing.T) {
	svc := newTestBillingService()
	tokens := UsageTokens{InputTokens: 120, OutputTokens: 30, CacheCreationTokens: 12, CacheReadTokens: 8}

	baseCost, err := svc.CalculateCost("claude-sonnet-4", tokens, 1.0)
	require.NoError(t, err)

	priorityCost, err := svc.CalculateCostWithServiceTier("claude-sonnet-4", tokens, 1.0, "priority")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*2, priorityCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*2, priorityCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*2, priorityCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*2, priorityCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*2, priorityCost.TotalCost, 1e-10)
}

func TestBillingServiceGetModelPricing_UsesDynamicPriorityFields(t *testing.T) {
	fixture := &LiteLLMModelPricing{
		InputCostPerToken:               2.5e-6,
		InputCostPerTokenPriority:       5e-6,
		OutputCostPerToken:              15e-6,
		OutputCostPerTokenPriority:      30e-6,
		CacheCreationInputTokenCost:     2.5e-6,
		CacheReadInputTokenCost:         0.25e-6,
		CacheReadInputTokenCostPriority: 0.5e-6,
		LongContextInputTokenThreshold:  272000,
		LongContextInputCostMultiplier:  2.0,
		LongContextOutputCostMultiplier: 1.5,
	}
	pricingSvc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"fixture-dynamic-priority": fixture,
		},
	}
	svc := NewBillingService(&config.Config{}, pricingSvc)

	pricing, err := svc.GetModelPricing("fixture-dynamic-priority")
	require.NoError(t, err)
	requireModelPricingMatches(t, tkModelPricingFromLiteLLM(fixture), pricing)
}

func TestBillingServiceGetModelPricing_OpenAIGpt52AliasesUseRegistryOwner(t *testing.T) {
	svc := newTestBillingService()
	want := mustRegistryOwnerModelPricing(t, "gpt-5.2")

	gpt52, err := svc.GetModelPricing("gpt-5.2")
	require.NoError(t, err)
	requireModelPricingMatches(t, want, gpt52)

	gpt52Codex, err := svc.GetModelPricing("gpt-5.2-codex")
	require.NoError(t, err)
	requireModelPricingMatches(t, want, gpt52Codex)
}

func TestCalculateCostWithServiceTier_PriorityFallsBackToTierMultiplierWhenExplicitPriceMissing(t *testing.T) {
	svc := NewBillingService(&config.Config{}, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"custom-no-priority": {
				InputCostPerToken:           1e-6,
				OutputCostPerToken:          2e-6,
				CacheCreationInputTokenCost: 0.5e-6,
				CacheReadInputTokenCost:     0.25e-6,
			},
		},
	})
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 40, CacheReadTokens: 20}

	baseCost, err := svc.CalculateCost("custom-no-priority", tokens, 1.0)
	require.NoError(t, err)

	priorityCost, err := svc.CalculateCostWithServiceTier("custom-no-priority", tokens, 1.0, "priority")
	require.NoError(t, err)

	require.InDelta(t, baseCost.InputCost*2, priorityCost.InputCost, 1e-10)
	require.InDelta(t, baseCost.OutputCost*2, priorityCost.OutputCost, 1e-10)
	require.InDelta(t, baseCost.CacheCreationCost*2, priorityCost.CacheCreationCost, 1e-10)
	require.InDelta(t, baseCost.CacheReadCost*2, priorityCost.CacheReadCost, 1e-10)
	require.InDelta(t, baseCost.TotalCost*2, priorityCost.TotalCost, 1e-10)
}

func TestGetModelPricing_OpenAIGpt52AliasesExposeRegistryPriorityPrices(t *testing.T) {
	svc := newTestBillingService()
	want := mustRegistryOwnerModelPricing(t, "gpt-5.2")

	gpt52, err := svc.GetModelPricing("gpt-5.2")
	require.NoError(t, err)
	requireModelPricingMatches(t, want, gpt52)

	gpt52Codex, err := svc.GetModelPricing("gpt-5.2-codex")
	require.NoError(t, err)
	requireModelPricingMatches(t, want, gpt52Codex)
}

func TestGetModelPricing_MapsDynamicPriorityFieldsIntoBillingPricing(t *testing.T) {
	svc := NewBillingService(&config.Config{}, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"dynamic-tier-model": {
				InputCostPerToken:                   1e-6,
				InputCostPerTokenPriority:           2e-6,
				OutputCostPerToken:                  3e-6,
				OutputCostPerTokenPriority:          6e-6,
				CacheCreationInputTokenCost:         4e-6,
				CacheCreationInputTokenCostAbove1hr: 5e-6,
				CacheReadInputTokenCost:             7e-7,
				CacheReadInputTokenCostPriority:     8e-7,
				LongContextInputTokenThreshold:      999,
				LongContextInputCostMultiplier:      1.5,
				LongContextOutputCostMultiplier:     1.25,
			},
		},
	})

	pricing, err := svc.GetModelPricing("dynamic-tier-model")
	require.NoError(t, err)
	require.InDelta(t, 1e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 2e-6, pricing.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 3e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 6e-6, pricing.OutputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 4e-6, pricing.CacheCreation5mPrice, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreation1hPrice, 1e-12)
	require.True(t, pricing.SupportsCacheBreakdown)
	require.InDelta(t, 7e-7, pricing.CacheReadPricePerToken, 1e-12)
	require.InDelta(t, 8e-7, pricing.CacheReadPricePerTokenPriority, 1e-12)
	require.Equal(t, 999, pricing.LongContextInputThreshold)
	require.InDelta(t, 1.5, pricing.LongContextInputMultiplier, 1e-12)
	require.InDelta(t, 1.25, pricing.LongContextOutputMultiplier, 1e-12)
}

// ---------------------------------------------------------------------------
// GetModelPricingWithChannel
// ---------------------------------------------------------------------------

func TestGetModelPricingWithChannel_NilChannelPricing_ReturnsOriginal(t *testing.T) {
	svc := newTestBillingService()

	pricing, err := svc.GetModelPricingWithChannel("claude-sonnet-4", nil)
	require.NoError(t, err)
	require.NotNil(t, pricing)

	// Should be identical to GetModelPricing
	original, err := svc.GetModelPricing("claude-sonnet-4")
	require.NoError(t, err)
	require.InDelta(t, original.InputPricePerToken, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, original.OutputPricePerToken, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, original.CacheCreationPricePerToken, pricing.CacheCreationPricePerToken, 1e-12)
	require.InDelta(t, original.CacheReadPricePerToken, pricing.CacheReadPricePerToken, 1e-12)
}

func TestGetModelPricingWithChannel_OverrideInputPriceOnly(t *testing.T) {
	svc := newTestBillingService()
	owner, err := svc.GetModelPricing("claude-sonnet-4")
	require.NoError(t, err)

	chPricing := &ChannelModelPricing{
		InputPrice: testPtrFloat64(99e-6),
	}
	pricing, err := svc.GetModelPricingWithChannel("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	// InputPrice overridden (both normal and priority)
	require.InDelta(t, 99e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 99e-6, pricing.InputPricePerTokenPriority, 1e-12)

	// Fields outside the scoped override continue to inherit the registry owner.
	require.InDelta(t, owner.OutputPricePerToken, pricing.OutputPricePerToken, 1e-12)
}

func TestGetModelPricingWithChannel_OverrideOutputPriceOnly(t *testing.T) {
	svc := newTestBillingService()
	owner, err := svc.GetModelPricing("claude-sonnet-4")
	require.NoError(t, err)

	chPricing := &ChannelModelPricing{
		OutputPrice: testPtrFloat64(88e-6),
	}
	pricing, err := svc.GetModelPricingWithChannel("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	// OutputPrice overridden
	require.InDelta(t, 88e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 88e-6, pricing.OutputPricePerTokenPriority, 1e-12)

	require.InDelta(t, owner.InputPricePerToken, pricing.InputPricePerToken, 1e-12)
}

func TestGetModelPricingWithChannel_OverrideAllFields(t *testing.T) {
	svc := newTestBillingService()

	chPricing := &ChannelModelPricing{
		InputPrice:       testPtrFloat64(10e-6),
		OutputPrice:      testPtrFloat64(20e-6),
		CacheWritePrice:  testPtrFloat64(5e-6),
		CacheReadPrice:   testPtrFloat64(1e-6),
		ImageOutputPrice: testPtrFloat64(50e-6),
	}
	pricing, err := svc.GetModelPricingWithChannel("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	require.InDelta(t, 10e-6, pricing.InputPricePerToken, 1e-12)
	require.InDelta(t, 10e-6, pricing.InputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 20e-6, pricing.OutputPricePerToken, 1e-12)
	require.InDelta(t, 20e-6, pricing.OutputPricePerTokenPriority, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreationPricePerToken, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreation5mPrice, 1e-12)
	require.InDelta(t, 5e-6, pricing.CacheCreation1hPrice, 1e-12)
	require.InDelta(t, 1e-6, pricing.CacheReadPricePerToken, 1e-12)
	require.InDelta(t, 1e-6, pricing.CacheReadPricePerTokenPriority, 1e-12)
	require.InDelta(t, 50e-6, pricing.ImageOutputPricePerToken, 1e-12)
}

func TestGetModelPricingWithChannel_CacheWritePriceAffects5mAnd1h(t *testing.T) {
	svc := newTestBillingService()

	chPricing := &ChannelModelPricing{
		CacheWritePrice: testPtrFloat64(7e-6),
	}
	pricing, err := svc.GetModelPricingWithChannel("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	// CacheWritePrice should set all three: CacheCreationPricePerToken, 5m, and 1h
	require.InDelta(t, 7e-6, pricing.CacheCreationPricePerToken, 1e-12)
	require.InDelta(t, 7e-6, pricing.CacheCreation5mPrice, 1e-12)
	require.InDelta(t, 7e-6, pricing.CacheCreation1hPrice, 1e-12)
}

func TestGetModelPricingWithChannel_CacheReadPriceAffectsPriority(t *testing.T) {
	svc := newTestBillingService()

	chPricing := &ChannelModelPricing{
		CacheReadPrice: testPtrFloat64(2e-6),
	}
	pricing, err := svc.GetModelPricingWithChannel("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	// CacheReadPrice should set both normal and priority
	require.InDelta(t, 2e-6, pricing.CacheReadPricePerToken, 1e-12)
	require.InDelta(t, 2e-6, pricing.CacheReadPricePerTokenPriority, 1e-12)
}

func TestGetModelPricingWithChannel_UnknownModelReturnsError(t *testing.T) {
	svc := newTestBillingService()

	chPricing := &ChannelModelPricing{
		InputPrice: testPtrFloat64(1e-6),
	}
	pricing, err := svc.GetModelPricingWithChannel("totally-unknown-model", chPricing)
	require.Error(t, err)
	require.Nil(t, pricing)
	require.Contains(t, err.Error(), "pricing not found")
}

func TestGetModelPricingWithChannel_NilImageOutputPriceZerosAndMarksExplicit(t *testing.T) {
	svc := newTestBillingService()

	chPricing := &ChannelModelPricing{
		InputPrice:  testPtrFloat64(10e-6),
		OutputPrice: testPtrFloat64(20e-6),
		// ImageOutputPrice intentionally nil
	}
	pricing, err := svc.GetModelPricingWithChannel("claude-sonnet-4", chPricing)
	require.NoError(t, err)

	require.Equal(t, 0.0, pricing.ImageOutputPricePerToken)
	require.True(t, pricing.ImageOutputPriceExplicit)
}

func TestComputeTokenBreakdown_ExplicitZeroImagePriceDoesNotInheritOutput(t *testing.T) {
	svc := newTestBillingService()

	pricing := &ModelPricing{
		InputPricePerToken:       3e-6,
		OutputPricePerToken:      15e-6,
		ImageOutputPricePerToken: 0,
		ImageOutputPriceExplicit: true,
	}
	tokens := UsageTokens{
		InputTokens:       100,
		OutputTokens:      200,
		ImageOutputTokens: 50,
	}
	bd := svc.computeTokenBreakdown(pricing, tokens, 1.0, "", false, false)

	// ImageOutputTokens should NOT fall back to outputPrice
	require.Equal(t, 0.0, bd.ImageOutputCost)
	// textOutputTokens = 200 - 50 = 150
	require.InDelta(t, 150*15e-6, bd.OutputCost, 1e-12)
}

func TestComputeTokenBreakdown_NonExplicitZeroImagePrice_FallsBackToOutput(t *testing.T) {
	svc := newTestBillingService()

	pricing := &ModelPricing{
		InputPricePerToken:       3e-6,
		OutputPricePerToken:      15e-6,
		ImageOutputPricePerToken: 0,
		ImageOutputPriceExplicit: false,
	}
	tokens := UsageTokens{
		InputTokens:       100,
		OutputTokens:      200,
		ImageOutputTokens: 50,
	}
	bd := svc.computeTokenBreakdown(pricing, tokens, 1.0, "", false, false)

	// Should fall back to outputPrice since not explicit
	require.InDelta(t, 50*15e-6, bd.ImageOutputCost, 1e-12)
	// textOutputTokens = 200 - 50 = 150
	require.InDelta(t, 150*15e-6, bd.OutputCost, 1e-12)
}

// TestComputeTokenBreakdown_ThinkingOutputPrice mirrors the Alibaba DashScope
// two-rate model (qwen3-8b/14b/32b: one id, output billed higher in thinking
// mode). enableThinking selects ThinkingOutputPricePerToken over the
// non-thinking OutputPricePerToken; with the field unset it must be a no-op.
func TestComputeTokenBreakdown_ThinkingOutputPrice(t *testing.T) {
	svc := newTestBillingService()

	// qwen3-8b: out ¥2/M non-thinking, ¥5/M thinking (÷6.7 → USD per token).
	pricing := &ModelPricing{
		InputPricePerToken:          tkCNYPerMTokToUSDPerToken(0.5),
		OutputPricePerToken:         tkCNYPerMTokToUSDPerToken(2.0),
		ThinkingOutputPricePerToken: tkCNYPerMTokToUSDPerToken(5.0),
	}
	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 1000}

	nonThinking := svc.computeTokenBreakdown(pricing, tokens, 1.0, "", false, false)
	thinking := svc.computeTokenBreakdown(pricing, tokens, 1.0, "", true, false)

	require.InDelta(t, 1000*tkCNYPerMTokToUSDPerToken(2.0), nonThinking.OutputCost, 1e-15,
		"non-thinking must bill the lower output rate")
	require.InDelta(t, 1000*tkCNYPerMTokToUSDPerToken(5.0), thinking.OutputCost, 1e-15,
		"thinking must bill the higher output rate")
	require.Greater(t, thinking.OutputCost, nonThinking.OutputCost,
		"thinking output cost must exceed non-thinking")
	// Input is mode-independent.
	require.InDelta(t, nonThinking.InputCost, thinking.InputCost, 1e-18)

	// No thinking rate configured → enableThinking is a no-op (other models).
	flat := &ModelPricing{InputPricePerToken: 1e-6, OutputPricePerToken: 3e-6}
	a := svc.computeTokenBreakdown(flat, tokens, 1.0, "", false, false)
	b := svc.computeTokenBreakdown(flat, tokens, 1.0, "", true, false)
	require.InDelta(t, a.OutputCost, b.OutputCost, 1e-18,
		"models without a thinking rate must be unaffected by enableThinking")
}

// TestTKPricingRegistry_Qwen3DenseThinkingRate guards that the registry carries
// a thinking output rate higher than the non-thinking one for the three
// Qwen3 dense models — the default-mode price (enable_thinking defaults to true).
func TestTKPricingRegistry_Qwen3DenseThinkingRate(t *testing.T) {
	registry := loadTKPricingOverlay()
	for _, id := range []string{"qwen3-8b", "qwen3-14b", "qwen3-32b"} {
		p := registry[id]
		require.NotNil(t, p, "registry must carry %s", id)
		require.Greater(t, p.OutputCostPerToken, 0.0, "%s non-thinking output > 0", id)
		require.Greater(t, p.ThinkingOutputCostPerToken, p.OutputCostPerToken,
			"%s thinking output rate must exceed non-thinking", id)
	}
}
