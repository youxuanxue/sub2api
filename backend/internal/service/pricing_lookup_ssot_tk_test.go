//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPricingLookupDeclaredAliasesShareIdentification(t *testing.T) {
	billing := activeRegistryBillingService(t)
	check := func() {
		t.Helper()
		snapshot := loadTKPricingOverlaySnapshot()
		for alias, owner := range snapshot.Aliases {
			if snapshot.Models[owner].TokenPricingAbsent {
				continue
			}
			for _, model := range []string{alias, "vendor/" + alias} {
				require.True(t, billing.HasIdentifiedTokenPricing(model), model)
				require.Equal(t, billing.pricingService.GetModelPricing(owner), billing.pricingService.GetIdentifiedModelPricing(model), model)
			}
		}
		for _, model := range []string{"claude-opus-4-audit-unknown", "gpt-audit-unknown", "unknown-vendor-audit"} {
			require.False(t, billing.HasIdentifiedTokenPricing(model), model)
		}
	}
	check()
	// The incident alias is a spelling boundary, not another catalog list.
	envelope := registryEnvelopeForTest(t, func(registry map[string]any) {
		registry["_aliases"].(map[string]any)["gpt-5.6"] = "gpt-5.4"
	}, nil)
	rebuildTKOverlayUnion([]byte(envelope))
	check()
}

func TestPricingLookupFamilyOwnerIsDeterministic(t *testing.T) {
	// Different rates expose accidental matching of a child variant. Expected
	// owner follows the existing family order, independently of map iteration.
	for _, bare := range []bool{false, true} {
		owner := &LiteLLMModelPricing{InputCostPerToken: .001, OutputCostPerToken: .002}
		variant := &LiteLLMModelPricing{InputCostPerToken: .003, OutputCostPerToken: .004}
		prices := map[string]*LiteLLMModelPricing{
			"claude-opus-4-20250514": owner,
			"claude-opus-4-5":        variant,
			"claude-opus-4-8":        variant,
		}
		if bare {
			prices["claude-opus-4"] = owner
		}
		svc := &PricingService{pricingData: prices}
		for range 100 {
			require.Equal(t, owner, svc.GetModelPricing("claude-opus-4-audit-unknown"))
		}
		require.Equal(t, variant, svc.GetModelPricing("claude-opus-4-5"), "exact owner still wins")
		require.Nil(t, svc.GetIdentifiedModelPricing("claude-opus-4-audit-unknown"))
	}
}

func TestPricingLookupDatedOwnerPrecedence(t *testing.T) {
	old := &LiteLLMModelPricing{InputCostPerToken: .001}
	latest := &LiteLLMModelPricing{InputCostPerToken: .002}
	bare := &LiteLLMModelPricing{InputCostPerToken: .003}
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"fixture-model-20250101": old, "fixture-model-20250201": latest,
	}}
	for range 100 {
		require.Equal(t, latest, svc.GetModelPricing("fixture-model-20250301"))
		require.Equal(t, latest, svc.GetIdentifiedModelPricing("fixture-model-20250301"))
	}
	svc.pricingData["fixture-model"] = bare
	require.Equal(t, bare, svc.GetModelPricing("fixture-model-20250301"))
	require.Equal(t, bare, svc.GetIdentifiedModelPricing("fixture-model-20250301"))
	require.Equal(t, old, svc.GetModelPricing("fixture-model-20250101"), "an exact snapshot keeps its own price")
}

func TestResponseModelBillingDeclaredAliasMatchesOwner(t *testing.T) {
	billing := activeRegistryBillingService(t)
	alias := "gpt-5.6"
	owner := loadTKPricingOverlaySnapshot().Aliases[alias]
	require.NotEmpty(t, owner)
	// Controlled prices isolate the name-resolution regression from live prices.
	envelope := registryEnvelopeForTest(t, func(registry map[string]any) {
		for model, rate := range map[string]float64{owner: .001, "gpt-6-astra": .002} {
			entry := registry[model].(map[string]any)
			entry["input_cost_per_token"], entry["output_cost_per_token"] = rate, rate*2
		}
	}, nil)
	rebuildTKOverlayUnion([]byte(envelope))
	tokens := UsageTokens{InputTokens: 100, OutputTokens: 50}
	for _, openai := range []bool{false, true} {
		for _, response := range []string{owner, alias} {
			usage := &openAIRecordUsageLogRepoStub{inserted: true}
			user := &openAIRecordUsageUserRepoStub{}
			fields := ChannelUsageFields{ChannelID: 9, OriginalModel: "gpt-6-astra", ChannelMappedModel: "gpt-6-astra", BillingModelSource: BillingModelSourceResponse}
			key := &APIKey{ID: 501, Quota: 100}
			var err error
			if openai {
				svc := newOpenAIRecordUsageServiceForTest(usage, user, &openAIRecordUsageSubRepoStub{}, nil)
				svc.billingService = billing
				err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
					Result: &OpenAIForwardResult{RequestID: "alias-openai-" + response, Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 50}, Model: "gpt-6-astra", UpstreamResponseModel: response, Duration: time.Second},
					APIKey: key, User: &User{ID: 601}, Account: &Account{ID: 701}, ChannelUsageFields: fields,
				})
			} else {
				svc := newGatewayRecordUsageServiceForTest(usage, user, &openAIRecordUsageSubRepoStub{})
				svc.billingService = billing
				err = svc.RecordUsage(context.Background(), &RecordUsageInput{
					Result: &ForwardResult{RequestID: "alias-gateway-" + response, Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 50}, Model: "gpt-6-astra", UpstreamResponseModel: response, Duration: time.Second},
					APIKey: key, User: &User{ID: 601}, Account: &Account{ID: 701}, ChannelUsageFields: fields,
				})
			}
			require.NoError(t, err)
			expected, err := billing.CalculateCost(owner, tokens, 1.1)
			require.NoError(t, err)
			require.InDelta(t, expected.ActualCost, usage.lastLog.ActualCost, 1e-12, "openai=%t response=%s", openai, response)
			require.InDelta(t, expected.ActualCost, user.lastAmount, 1e-12)
		}
	}
}
