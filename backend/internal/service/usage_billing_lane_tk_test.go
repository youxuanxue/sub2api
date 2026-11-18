//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type laneTKCapabilityRepo struct {
	laneTestRepo
	hold         *HoldCommand
	video        *VideoRefundCommand
	released     string
	expiredAt    time.Time
	expiredBatch int
	err          error
}

func (r *laneTKCapabilityRepo) ReserveBalanceHold(_ context.Context, cmd *HoldCommand) (bool, error) {
	r.hold = cmd
	return true, r.err
}
func (r *laneTKCapabilityRepo) ReleaseBalanceHold(_ context.Context, id string) (bool, error) {
	r.released = id
	return true, r.err
}
func (r *laneTKCapabilityRepo) ReleaseExpiredBalanceHolds(_ context.Context, at time.Time, batch int) (int, error) {
	r.expiredAt = at
	r.expiredBatch = batch
	return 2, r.err
}
func (r *laneTKCapabilityRepo) ApplyVideoRefund(_ context.Context, cmd *VideoRefundCommand) (bool, error) {
	r.video = cmd
	return true, r.err
}

func TestBillingLaneForwardsTokenKeyMoneyMovementCapabilities(t *testing.T) {
	for _, wantErr := range []error{nil, errors.New("database unavailable")} {
		repo := &laneTKCapabilityRepo{err: wantErr}
		lane := NewUsageBillingLaneCombiner(repo)
		ctx := context.Background()
		hold := &HoldCommand{RequestID: "held", Amount: 2}
		reserved, err := lane.ReserveBalanceHold(ctx, hold)
		require.Equal(t, wantErr, err)
		require.True(t, reserved)
		require.Same(t, hold, repo.hold)
		released, err := lane.ReleaseBalanceHold(ctx, "held")
		require.Equal(t, wantErr, err)
		require.True(t, released)
		require.Equal(t, "held", repo.released)
		at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
		count, err := lane.ReleaseExpiredBalanceHolds(ctx, at, 50)
		require.Equal(t, wantErr, err)
		require.Equal(t, 2, count)
		require.Equal(t, at, repo.expiredAt)
		require.Equal(t, 50, repo.expiredBatch)
		video := &VideoRefundCommand{}
		refunded, err := lane.ApplyVideoRefund(ctx, video)
		require.Equal(t, wantErr, err)
		require.True(t, refunded)
		require.Same(t, video, repo.video)
	}
}

func TestBillingLaneMissingTokenKeyCapabilityFailsClosed(t *testing.T) {
	lane := NewUsageBillingLaneCombiner(&laneTestRepo{})
	ok, err := lane.ReserveBalanceHold(context.Background(), &HoldCommand{Amount: 1})
	require.False(t, ok)
	require.ErrorContains(t, err, "lacks balance hold capability")
	ok, err = lane.ApplyVideoRefund(context.Background(), &VideoRefundCommand{})
	require.False(t, ok)
	require.ErrorContains(t, err, "lacks video refund capability")
}
