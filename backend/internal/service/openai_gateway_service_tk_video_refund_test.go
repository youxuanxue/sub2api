//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildVideoRefundFromUsage_BalanceMode(t *testing.T) {
	groupID := int64(3)
	seconds := int64(8)
	rec := &VideoTaskRecord{PublicTaskID: "vt_abc", UserID: 7, APIKeyID: 11}
	orig := &UsageLog{
		UserID:               7,
		APIKeyID:             11,
		AccountID:            42,
		RequestID:            "local:req-1",
		Model:                "doubao-seedance-1-0-pro-250528",
		RequestedModel:       "doubao-seedance-1-0-pro-250528",
		GroupID:              &groupID,
		BillingType:          BillingTypeBalance,
		TotalCost:            0.87,
		ActualCost:           0.87,
		RateMultiplier:       1.0,
		VideoDurationSeconds: &seconds,
	}

	cmd, refundLog := buildVideoRefundFromUsage(rec, orig)
	require.NotNil(t, cmd)
	require.NotNil(t, refundLog)

	require.Equal(t, TkVideoRefundRequestIDPrefix+"vt_abc", cmd.RequestID)
	require.Equal(t, int64(7), cmd.UserID)
	require.Equal(t, int64(11), cmd.APIKeyID)
	require.Equal(t, BillingTypeBalance, cmd.BillingType)
	require.Nil(t, cmd.SubscriptionID)
	require.InDelta(t, 0.87, cmd.Amount, 1e-12)

	// Double-entry mirror: the pair must SUM to zero.
	require.InDelta(t, -0.87, refundLog.ActualCost, 1e-12)
	require.InDelta(t, -0.87, refundLog.TotalCost, 1e-12)
	require.NotNil(t, refundLog.VideoDurationSeconds)
	require.Equal(t, int64(-8), *refundLog.VideoDurationSeconds)
	require.NotNil(t, refundLog.BillingMode)
	require.Equal(t, TkBillingModeVideoRefund, *refundLog.BillingMode)
	require.Equal(t, cmd.RequestID, refundLog.RequestID)
	require.Equal(t, orig.AccountID, refundLog.AccountID)
	require.Equal(t, orig.GroupID, refundLog.GroupID)
}

func TestBuildVideoRefundFromUsage_SubscriptionMode(t *testing.T) {
	subID := int64(99)
	rec := &VideoTaskRecord{PublicTaskID: "vt_sub", UserID: 7, APIKeyID: 11}
	orig := &UsageLog{
		UserID:         7,
		APIKeyID:       11,
		SubscriptionID: &subID,
		BillingType:    BillingTypeSubscription,
		TotalCost:      1.16,
		ActualCost:     0.58, // multiplier 0.5 — refund returns what was charged
		RateMultiplier: 0.5,
	}

	cmd, refundLog := buildVideoRefundFromUsage(rec, orig)
	require.NotNil(t, cmd)
	require.Equal(t, BillingTypeSubscription, cmd.BillingType)
	require.NotNil(t, cmd.SubscriptionID)
	require.Equal(t, subID, *cmd.SubscriptionID)
	require.InDelta(t, 0.58, cmd.Amount, 1e-12)
	require.InDelta(t, -0.58, refundLog.ActualCost, 1e-12)
	// Seconds were never recorded on the original (legacy row) → stays nil.
	require.Nil(t, refundLog.VideoDurationSeconds)
}

func TestBuildVideoRefundFromUsage_NothingToRefund(t *testing.T) {
	rec := &VideoTaskRecord{PublicTaskID: "vt_free"}
	// Unpriced model billed $0 at submit — nothing to reverse.
	cmd, refundLog := buildVideoRefundFromUsage(rec, &UsageLog{ActualCost: 0})
	require.Nil(t, cmd)
	require.Nil(t, refundLog)
	// Free-subscription shape: TotalCost > 0 but multiplier 0 → ActualCost 0.
	cmd, _ = buildVideoRefundFromUsage(rec, &UsageLog{TotalCost: 0.5, ActualCost: 0})
	require.Nil(t, cmd)
	cmd, _ = buildVideoRefundFromUsage(nil, &UsageLog{ActualCost: 1})
	require.Nil(t, cmd)
	cmd, _ = buildVideoRefundFromUsage(rec, nil)
	require.Nil(t, cmd)
}
