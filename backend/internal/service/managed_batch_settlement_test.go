package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

func TestManagedBatchSettlementUsesAuthorizedBillingPath(t *testing.T) {
	for _, mode := range []string{RoutingModeDirect, RoutingModeUniversal} {
		for _, typ := range []string{SubscriptionTypeStandard, SubscriptionTypeSubscription} {
			t.Run(mode+"/"+typ, func(t *testing.T) {
				logs := &openAIRecordUsageLogRepoStub{inserted: true}
				billing := &openAIRecordUsageBillingRepoStub{}
				settlement := OpenAIManagedBatchSettlement{Gateway: newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, billing, &openAIRecordUsageUserRepoStub{}, nil, nil)}
				group := &Group{ID: 88, SubscriptionType: typ, RateMultiplier: 1}
				in := ManagedBatchSettlementInput{
					RequestID: "managed-batch:b:item:12", Model: "gpt-5.1", BillingTier: UsageEstimatedBillingTier,
					Account: &Account{ID: 3}, APIKey: &APIKey{ID: 1, UserID: 2, RoutingMode: mode, GroupID: &group.ID, Group: group},
					Usage: OpenAIUsage{InputTokens: 20, CacheReadInputTokens: 5, OutputTokens: 7},
				}
				if group.IsSubscriptionType() {
					in.Subscription = &UserSubscription{ID: 4, UserID: 2, GroupID: group.ID}
				}
				mapping := ChannelMappingResult{ChannelID: 19}
				candidateCtx := context.WithValue(context.Background(), candidateRequestContextKey{}, &CandidateRequest{current: &candidateExecutionPath{channel: mapping}})
				for _, jobID := range []string{"first-attempt", "redelivery"} {
					ctx := context.WithValue(candidateCtx, ctxkey.RequestID, jobID)
					require.NoError(t, settlement.SettleManagedBatch(ctx, in))
					require.Equal(t, "local:"+in.RequestID, billing.lastCmd.RequestID)
					require.Equal(t, billing.lastCmd.RequestID, logs.lastLog.RequestID)
					require.Equal(t, &group.ID, logs.lastLog.GroupID)
					require.Equal(t, &mapping.ChannelID, logs.lastLog.ChannelID)
					require.Equal(t, &in.BillingTier, logs.lastLog.BillingTier)
					require.Equal(t, 15, billing.lastCmd.InputTokens)
					require.Equal(t, 5, billing.lastCmd.CacheReadTokens)
					require.Equal(t, 7, billing.lastCmd.OutputTokens)
					if in.Subscription != nil {
						require.Equal(t, &in.Subscription.ID, billing.lastCmd.SubscriptionID)
						require.Positive(t, billing.lastCmd.SubscriptionCost)
						require.Zero(t, billing.lastCmd.BalanceCost)
					} else {
						require.Nil(t, billing.lastCmd.SubscriptionID)
						require.Positive(t, billing.lastCmd.BalanceCost)
						require.Zero(t, billing.lastCmd.SubscriptionCost)
					}
				}
			})
		}
	}
}

func TestManagedBatchSettlementRejectsMismatchedAuthorization(t *testing.T) {
	group := &Group{ID: 8, SubscriptionType: SubscriptionTypeSubscription}
	in := ManagedBatchSettlementInput{RequestID: "item", Model: "m", APIKey: &APIKey{ID: 1, UserID: 2, Group: group, GroupID: &group.ID}, Account: &Account{ID: 3}}
	settlement := OpenAIManagedBatchSettlement{Gateway: &OpenAIGatewayService{}}
	for _, sub := range []*UserSubscription{nil, {ID: 4, UserID: 2, GroupID: 9}, {ID: 4, UserID: 1, GroupID: 8}} {
		in.Subscription = sub
		require.ErrorContains(t, settlement.SettleManagedBatch(context.Background(), in), "matching authorized group and subscription")
	}
}
