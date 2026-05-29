//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// upstream Wei-Shaw/sub2api#1833 / #1544: when a model has no LiteLLM entry and
// no channel/fallback pricing (e.g. GLM/qwen/deepseek attached to an
// anthropic-type group over /v1/messages), calculateTokenCost must not silently
// bill zero. It now returns a zero-cost breakdown explicitly marked with
// BillingMode (and emits a structured warning), mirroring the OpenAI path's
// observable pricing_missing_record_zero_cost behavior.
func TestCalculateTokenCost_PricingMissing_RecordsObservableZeroCost(t *testing.T) {
	svc := &GatewayService{billingService: NewBillingService(&config.Config{}, nil)}

	result := &ForwardResult{Model: "tk-nonexistent-model-zzz"}
	result.Usage.InputTokens = 100
	result.Usage.OutputTokens = 50
	apiKey := &APIKey{ID: 7, Group: &Group{ID: 3, Platform: PlatformAnthropic}}

	cost := svc.calculateTokenCost(context.Background(), result, apiKey, "tk-nonexistent-model-zzz", 1.0, &recordUsageOpts{})

	require.NotNil(t, cost)
	require.Equal(t, 0.0, cost.ActualCost, "request is not blocked — records zero like the OpenAI path")
	require.Equal(t, string(BillingModeToken), cost.BillingMode,
		"#1833/#1544: pricing-missing must be a marked/observable zero-cost record, not a silent ActualCost:0 leak")
}
