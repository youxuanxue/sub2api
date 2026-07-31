//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsOpenAIVideoUsageResult(t *testing.T) {
	require.False(t, isOpenAIVideoUsageResult(nil))
	require.False(t, isOpenAIVideoUsageResult(&OpenAIForwardResult{}))
	require.True(t, isOpenAIVideoUsageResult(&OpenAIForwardResult{VideoCount: 1}))
}

func TestOpenAIGatewayServiceRecordUsage_SeedanceVideoSubmitUsesTierPricing(t *testing.T) {
	const model = "doubao-seedance-2-0-260128"
	groupID := int64(1901)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:            "seedance-tier-settle",
			Model:                model,
			BillingModel:         model,
			UpstreamModel:        model,
			VideoCount:           1,
			VideoResolution:      VideoBillingResolution480P,
			VideoDurationSeconds: 5,
			Duration:             time.Second,
		},
		APIKey: &APIKey{
			ID:      501901,
			GroupID: i64p(groupID),
			Group: &Group{
				ID:             groupID,
				Platform:       PlatformNewAPI,
				RateMultiplier: 1,
			},
		},
		User:    &User{ID: 601901},
		Account: &Account{ID: 701901, Platform: PlatformNewAPI},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	unit480, ok := tkOverlayVideoUnitPriceUSD(model, VideoBillingResolution480P, nil)
	require.True(t, ok)
	require.InDelta(t, unit480*5, usageRepo.lastLog.TotalCost, 1e-9)
	require.InDelta(t, unit480*5, usageRepo.lastLog.ActualCost, 1e-9)
	require.NotNil(t, usageRepo.lastLog.BillingMode)
	require.Equal(t, string(BillingModeVideo), *usageRepo.lastLog.BillingMode)
	require.Equal(t, 1, usageRepo.lastLog.VideoCount)
	require.NotNil(t, usageRepo.lastLog.VideoResolution)
	require.Equal(t, VideoBillingResolution480P, *usageRepo.lastLog.VideoResolution)
	require.NotNil(t, usageRepo.lastLog.VideoDurationSeconds)
	require.Equal(t, int64(5), *usageRepo.lastLog.VideoDurationSeconds)
}

func TestOpenAIGatewayServiceRecordUsage_SeedanceVideoOverlayOverridesTokenChannel(t *testing.T) {
	const model = "doubao-seedance-2-0-260128"
	groupID := int64(1903)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.resolver = newOpenAITokenImageChannelPricingResolverForTest(t, groupID, model)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:            "seedance-token-channel-overlay",
			Model:                model,
			BillingModel:         model,
			VideoCount:           1,
			VideoResolution:      VideoBillingResolution480P,
			VideoDurationSeconds: 5,
			Duration:             time.Second,
		},
		APIKey: &APIKey{
			ID:      501903,
			GroupID: i64p(groupID),
			Group: &Group{
				ID:             groupID,
				Platform:       PlatformNewAPI,
				RateMultiplier: 1,
			},
		},
		User:    &User{ID: 601903},
		Account: &Account{ID: 701903, Platform: PlatformNewAPI},
	})

	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	unit480, ok := tkOverlayVideoUnitPriceUSD(model, VideoBillingResolution480P, nil)
	require.True(t, ok)
	require.NotNil(t, usageRepo.lastLog.BillingMode)
	require.Equal(t, string(BillingModeVideo), *usageRepo.lastLog.BillingMode)
	require.InDelta(t, unit480*5, usageRepo.lastLog.ActualCost, 1e-9)
}
