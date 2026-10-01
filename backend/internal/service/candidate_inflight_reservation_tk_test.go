//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCandidateInflightReservationRebindAndAsyncSettlement(t *testing.T) {
	router, _, key := globalCandidateFixture([]Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)},
		[]Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 20)})
	cache := newMemInflightCache(10)
	billing := newInflightSvc(t, cache, 60)
	router.candidateGateway.billingCacheService = billing
	ctx, state := prepareGlobalCandidate(t, router, key)
	var groups []int64
	done, err := ReserveCandidateInflight(ctx, billing, func(_ context.Context, current *APIKey) (float64, bool) {
		groups = append(groups, current.Group.ID)
		return float64(current.Group.ID) / 10, true
	})
	require.NoError(t, err)
	t.Cleanup(done)
	first := InflightReservationFromContext(ctx)
	require.Equal(t, 1.0, first.Amount())
	selection, err := router.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", map[int64]struct{}{1: {}}, "", key.UserID)
	require.NoError(t, err)
	t.Cleanup(selection.ReleaseFunc)
	require.Equal(t, int64(2), selection.Account.ID)
	require.Equal(t, []int64{10, 20}, groups)
	require.Equal(t, 2.0, InflightReservationFromContext(ctx).Amount())
	require.Equal(t, 1, cache.count())
	settled := InflightReservationFromContext(ctx).Acquire()
	done()
	require.False(t, state.balanceReserved)
	require.Nil(t, InflightReservationFromContext(ctx))
	require.Equal(t, 1, cache.count())
	settled()
	require.Equal(t, 0, cache.count())
	require.Equal(t, int32(2), cache.releaseCt.Load())
}

func TestCandidateInflightReservationRejectsUnpricedRebind(t *testing.T) {
	router, _, key := globalCandidateFixture([]Group{grp(10, PlatformOpenAI, 1, false), grp(20, PlatformOpenAI, 2, false)},
		[]Account{globalCandidateAccount(1, 1, 10), globalCandidateAccount(2, 2, 20)})
	cache := newMemInflightCache(10)
	billing := newInflightSvc(t, cache, 60)
	billing.cfg.Billing.InflightReservation.FailClosedOnUnpriced = true
	router.candidateGateway.billingCacheService = billing
	ctx, state := prepareGlobalCandidate(t, router, key)
	done, err := ReserveCandidateInflight(ctx, billing, func(_ context.Context, current *APIKey) (float64, bool) {
		return 1, current.Group.ID == 10
	})
	require.NoError(t, err)
	t.Cleanup(done)
	selection, err := router.candidateGateway.SelectAccountWithLoadAwareness(ctx, key.GroupID, "", "gpt-5.4", map[int64]struct{}{1: {}}, "", key.UserID)
	require.Nil(t, selection)
	require.ErrorIs(t, err, ErrInsufficientBalance)
	require.Equal(t, int64(10), *key.GroupID)
	require.False(t, state.balanceReserved)
	require.Nil(t, InflightReservationFromContext(ctx))
	require.Equal(t, 0, cache.count())
}
