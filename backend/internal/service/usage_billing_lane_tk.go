package service

import (
	"context"
	"fmt"
	"time"
)

// The DI billing decorator must retain TokenKey's narrow money-movement
// capabilities. Missing support fails closed instead of silently disabling holds.
func (c *UsageBillingLaneCombiner) ReserveBalanceHold(ctx context.Context, cmd *HoldCommand) (bool, error) {
	if owner, ok := c.repo.(UsageBillingHoldApplier); ok {
		return owner.ReserveBalanceHold(ctx, cmd)
	}
	return false, fmt.Errorf("billing repository lacks balance hold capability")
}

func (c *UsageBillingLaneCombiner) ReleaseBalanceHold(ctx context.Context, requestID string) (bool, error) {
	if owner, ok := c.repo.(UsageBillingHoldApplier); ok {
		return owner.ReleaseBalanceHold(ctx, requestID)
	}
	return false, fmt.Errorf("billing repository lacks balance hold capability")
}

func (c *UsageBillingLaneCombiner) ReleaseExpiredBalanceHolds(ctx context.Context, olderThan time.Time, batch int) (int, error) {
	if owner, ok := c.repo.(UsageBillingHoldApplier); ok {
		return owner.ReleaseExpiredBalanceHolds(ctx, olderThan, batch)
	}
	return 0, fmt.Errorf("billing repository lacks balance hold capability")
}

func (c *UsageBillingLaneCombiner) ApplyVideoRefund(ctx context.Context, cmd *VideoRefundCommand) (bool, error) {
	if owner, ok := c.repo.(UsageBillingVideoRefundApplier); ok {
		return owner.ApplyVideoRefund(ctx, cmd)
	}
	return false, fmt.Errorf("billing repository lacks video refund capability")
}
