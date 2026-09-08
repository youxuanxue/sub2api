//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestSettleBillingOnAccountServedModel_UsesAccountMappingSSOT(t *testing.T) {
	t.Parallel()
	account := aliTokenPlanAccountForBillingTest(t)
	require.Equal(t, "qwen3.7-plus", settleBillingOnAccountServedModel(account, "qwen-plus", "qwen-plus"))
	require.Equal(t, "qwen3.8-max", settleBillingOnAccountServedModel(account, "qwen-max", "qwen-max"))
	require.Equal(t, "claude-sonnet-4", settleBillingOnAccountServedModel(account, "claude-sonnet-4", "claude-sonnet-4"))

	payg := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"base_url": "https://dashscope.aliyuncs.com",
			"model_mapping": map[string]string{
				"qwen-plus": "qwen-plus",
			},
		},
	}
	require.Equal(t, "qwen-plus", settleBillingOnAccountServedModel(payg, "qwen-plus", "qwen-plus"))
	require.Equal(t, "qwen-plus", settleBillingOnAccountServedModel(nil, "qwen-plus", "qwen-plus"))
}

func TestGatewayRecordUsage_TokenPlanLegacyAliasBillsServedModel(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	account := aliTokenPlanAccountForBillingTest(t)

	expectedCost, err := svc.billingService.CalculateCost("qwen3.7-plus", UsageTokens{
		InputTokens:  1000,
		OutputTokens: 500,
	}, 1.1)
	require.NoError(t, err)
	legacyCost, err := svc.billingService.CalculateCost("qwen-plus", UsageTokens{
		InputTokens:  1000,
		OutputTokens: 500,
	}, 1.1)
	require.NoError(t, err)
	require.NotEqual(t, expectedCost.ActualCost, legacyCost.ActualCost,
		"test must distinguish legacy vs served price cards")

	err = svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:     "tokenplan-legacy-billing",
			Model:         "qwen-plus",
			UpstreamModel: "qwen3.7-plus",
			Usage: ClaudeUsage{
				InputTokens:  1000,
				OutputTokens: 500,
			},
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 10, Group: &Group{ID: 1, RateMultiplier: 1.1}},
		User:    &User{ID: 20},
		Account: account,
		ChannelUsageFields: ChannelUsageFields{
			OriginalModel:      "qwen-plus",
			ChannelMappedModel: "qwen-plus",
			BillingModelSource: BillingModelSourceRequested,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, expectedCost.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
	require.InDelta(t, expectedCost.ActualCost, userRepo.lastAmount, 1e-12)
}

// china / newapi Token Plan traffic settles on the OpenAI-compat RecordUsage
// path; BillingModelSourceRequested must not keep the legacy price card after
// account model_mapping remaps the request.
func TestOpenAIRecordUsage_TokenPlanLegacyAliasBillsServedModel(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	account := aliTokenPlanAccountForBillingTest(t)

	expectedCost, err := svc.billingService.CalculateCost("qwen3.7-plus", UsageTokens{
		InputTokens:  1000,
		OutputTokens: 500,
	}, 1.1)
	require.NoError(t, err)
	legacyCost, err := svc.billingService.CalculateCost("qwen-plus", UsageTokens{
		InputTokens:  1000,
		OutputTokens: 500,
	}, 1.1)
	require.NoError(t, err)
	require.NotEqual(t, expectedCost.ActualCost, legacyCost.ActualCost,
		"test must distinguish legacy vs served price cards")

	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:     "tokenplan-openai-legacy-billing",
			Model:         "qwen-plus",
			BillingModel:  "qwen-plus",
			UpstreamModel: "qwen3.7-plus",
			Usage: OpenAIUsage{
				InputTokens:  1000,
				OutputTokens: 500,
			},
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 10, Group: &Group{ID: 1, RateMultiplier: 1.1}},
		User:    &User{ID: 20},
		Account: account,
		ChannelUsageFields: ChannelUsageFields{
			OriginalModel:      "qwen-plus",
			ChannelMappedModel: "qwen-plus",
			BillingModelSource: BillingModelSourceRequested,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, expectedCost.ActualCost, usageRepo.lastLog.ActualCost, 1e-12)
	require.InDelta(t, expectedCost.ActualCost, userRepo.lastAmount, 1e-12)
}

func aliTokenPlanAccountForBillingTest(t *testing.T) *Account {
	t.Helper()
	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"base_url": newapiintegration.AliTokenPlanBaseURL,
		},
	}, nil, nil, nil)
	require.True(t, ok)
	return &Account{
		ID:          129,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"base_url":      newapiintegration.AliTokenPlanBaseURL,
			"model_mapping": modelMappingToAny(mapping),
		},
	}
}
