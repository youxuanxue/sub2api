//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolve_SubscriptionReadErrorFallback(t *testing.T) {
	lookupErr := errors.New("subscription database unavailable")
	for _, evaluated := range []bool{false, true} {
		for _, fallback := range []bool{false, true} {
			name := "compatibility"
			if evaluated {
				name = "candidate"
			}
			if fallback {
				name += "/with_balance"
			} else {
				name += "/without_balance"
			}
			t.Run(name, func(t *testing.T) {
				sub := grp(22, PlatformOpenAI, 90, true)
				groups := []Group{sub}
				if fallback {
					groups = append(groups, grp(2, PlatformOpenAI, 0, false))
				}
				resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: groups})
				resolver.SetSubscriptionUsability(NewSubscriptionService(groupRepoNoop{}, usableSubRepoStub{err: lookupErr}, nil, nil, nil))
				if evaluated {
					resolver.SetCandidateEvaluator(nil, func(_ context.Context, group Group, _ string, _ UniversalShape) (GroupCandidateEligibility, error) {
						require.False(t, group.IsSubscriptionType(), "unverified subscription must not reach account selection")
						return GroupCandidateEligibility{Supported: true, Available: true}, nil
					})
				}
				picked, err := resolver.Resolve(context.Background(), universalKey(31), ShapeOpenAIChat, "gpt-5", "")
				if !fallback {
					require.ErrorIs(t, err, lookupErr)
					require.Nil(t, picked)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, picked)
				require.Equal(t, int64(2), picked.ID)
				require.False(t, picked.IsSubscriptionType())
			})
		}
	}
}

func TestResolve_SubscriptionMaintenanceErrorFallsBack(t *testing.T) {
	now := time.Date(2026, 8, 21, 7, 39, 34, 0, time.UTC)
	start := now.Add(-28 * 24 * time.Hour)
	lateAnchor := now.Add(-3 * 24 * time.Hour)
	subscription := &UserSubscription{ID: 5, UserID: 31, GroupID: 22,
		Status: SubscriptionStatusActive, StartsAt: start, ExpiresAt: now.Add(24 * time.Hour),
		WeeklyWindowStart: &lateAnchor, WeeklyUsageUSD: 50.06}
	svc := NewSubscriptionService(groupRepoNoop{}, failingWeeklyResetRepo{usableSubRepoStub: usableSubRepoStub{sub: subscription}}, nil, nil, nil)
	svc.now = func() time.Time { return now }
	sub := grp(22, PlatformOpenAI, 90, true)
	limit := 50.0
	sub.WeeklyLimitUSD = &limit
	resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{sub, grp(2, PlatformOpenAI, 0, false)}})
	resolver.SetSubscriptionUsability(svc)
	picked, err := resolver.Resolve(context.Background(), universalKey(31), ShapeOpenAIChat, "gpt-5", "")
	require.NoError(t, err)
	require.NotNil(t, picked)
	require.Equal(t, int64(2), picked.ID)
}

func TestResolve_SubscriptionCandidateErrorFallback(t *testing.T) {
	lookupErr := errors.New("account snapshot unavailable")
	for _, balanceAvailable := range []bool{false, true} {
		name := "balance_unavailable"
		if balanceAvailable {
			name = "balance_available"
		}
		t.Run(name, func(t *testing.T) {
			resolver := NewUniversalRoutingResolver(&stubSpanLister{groups: []Group{grp(22, PlatformOpenAI, 90, true), grp(2, PlatformOpenAI, 0, false)}})
			resolver.SetCandidateEvaluator(nil, func(_ context.Context, group Group, _ string, _ UniversalShape) (GroupCandidateEligibility, error) {
				if group.IsSubscriptionType() {
					return GroupCandidateEligibility{}, lookupErr
				}
				return GroupCandidateEligibility{Supported: true, Available: balanceAvailable}, nil
			})
			picked, err := resolver.Resolve(context.Background(), universalKey(31), ShapeOpenAIChat, "gpt-5", "")
			if !balanceAvailable {
				require.ErrorIs(t, err, lookupErr)
				require.Nil(t, picked)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, picked)
			require.Equal(t, int64(2), picked.ID)
		})
	}
}
