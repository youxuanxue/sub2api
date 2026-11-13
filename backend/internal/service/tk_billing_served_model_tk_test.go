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
	require.Equal(t, "qwen4.0-max-preview", settleBillingOnAccountServedModel(account, "qwen4.0-max", "qwen4.0-max"))
	require.Equal(t, "qwen-plus", settleBillingOnAccountServedModel(account, "qwen-plus", "qwen-plus"))
	require.Equal(t, "qwen-max", settleBillingOnAccountServedModel(account, "qwen-max", "qwen-max"))
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

	expectedCost, err := svc.billingService.CalculateCost("qwen4.0-max-preview", UsageTokens{
		InputTokens:  1000,
		OutputTokens: 500,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID:     "tokenplan-qwen4-billing",
			Model:         "qwen4.0-max",
			UpstreamModel: "qwen4.0-max-preview",
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
			OriginalModel:      "qwen4.0-max",
			ChannelMappedModel: "qwen4.0-max",
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

	expectedCost, err := svc.billingService.CalculateCost("qwen4.0-max-preview", UsageTokens{
		InputTokens:  1000,
		OutputTokens: 500,
	}, 1.1)
	require.NoError(t, err)

	err = svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:     "tokenplan-openai-qwen4-billing",
			Model:         "qwen4.0-max",
			BillingModel:  "qwen4.0-max",
			UpstreamModel: "qwen4.0-max-preview",
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
			OriginalModel:      "qwen4.0-max",
			ChannelMappedModel: "qwen4.0-max",
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
