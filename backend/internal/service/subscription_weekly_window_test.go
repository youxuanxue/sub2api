//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestValidateAndCheckLimits_LateWeeklyAnchorRecoversStartsAtAlignedWeek(t *testing.T) {
	// Repro: 30-day invite trial (user 31 / group 22). Weekly window was
	// re-anchored to first-activate/admin-reset time, so the rolling next
	// reset falls after ExpiresAt. A StartsAt-aligned week has already begun
	// and must refresh instead of returning WEEKLY_LIMIT_EXCEEDED.
	startsAt := time.Date(2026, 7, 24, 4, 31, 4, 81236000, time.UTC)
	expiresAt := startsAt.Add(30 * 24 * time.Hour)
	lateAnchor := time.Date(2026, 8, 18, 6, 22, 6, 0, time.UTC)
	now := time.Date(2026, 8, 21, 7, 39, 34, 0, time.UTC)
	limit := 50.0
	sub := &UserSubscription{
		Status:            SubscriptionStatusActive,
		StartsAt:          startsAt,
		ExpiresAt:         expiresAt,
		WeeklyWindowStart: &lateAnchor,
		WeeklyUsageUSD:    50.06,
	}
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil)
	svc.now = func() time.Time { return now }

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub, &Group{WeeklyLimitUSD: &limit})

	require.NoError(t, err)
	require.True(t, needsMaintenance)
	require.Zero(t, sub.WeeklyUsageUSD)
}

func TestValidateAndCheckLimits_LateWeeklyAnchorStillBlocksInsideSameStartsAtWeek(t *testing.T) {
	startsAt := time.Date(2026, 7, 24, 4, 31, 4, 0, time.UTC)
	expiresAt := startsAt.Add(30 * 24 * time.Hour)
	lateAnchor := time.Date(2026, 8, 18, 6, 22, 6, 0, time.UTC)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	limit := 50.0
	sub := &UserSubscription{
		Status:            SubscriptionStatusActive,
		StartsAt:          startsAt,
		ExpiresAt:         expiresAt,
		WeeklyWindowStart: &lateAnchor,
		WeeklyUsageUSD:    50.06,
	}
	svc := NewSubscriptionService(groupRepoNoop{}, userSubRepoNoop{}, nil, nil, nil)
	svc.now = func() time.Time { return now }

	needsMaintenance, err := svc.ValidateAndCheckLimits(sub, &Group{WeeklyLimitUSD: &limit})

	require.ErrorIs(t, err, ErrWeeklyLimitExceeded)
	require.False(t, needsMaintenance)
	require.Equal(t, 50.06, sub.WeeklyUsageUSD)
}

func TestCheckAndResetWindows_LateWeeklyAnchorAdvancesToStartsAtAlignedWeek(t *testing.T) {
	startsAt := time.Date(2026, 7, 24, 4, 31, 4, 81236000, time.UTC)
	lateAnchor := time.Date(2026, 8, 18, 6, 22, 6, 0, time.UTC)
	now := time.Date(2026, 8, 21, 7, 39, 34, 0, time.UTC)
	repo := &weeklyResetUserSubRepo{}
	svc := NewSubscriptionService(groupRepoNoop{}, repo, nil, nil, nil)
	svc.now = func() time.Time { return now }
	sub := &UserSubscription{
		ID:                5,
		StartsAt:          startsAt,
		ExpiresAt:         startsAt.Add(30 * 24 * time.Hour),
		WeeklyWindowStart: &lateAnchor,
		WeeklyUsageUSD:    50.06,
	}

	require.NoError(t, svc.CheckAndResetWindows(context.Background(), sub))
	require.True(t, repo.resetCalled)
	require.Equal(t, startsAt.Add(28*24*time.Hour), repo.resetAt)
	require.Zero(t, sub.WeeklyUsageUSD)
	require.Equal(t, startsAt.Add(28*24*time.Hour), *sub.WeeklyWindowStart)
}

type weeklyResetUserSubRepo struct {
	userSubRepoNoop
	resetCalled bool
	resetAt     time.Time
}

func (r *weeklyResetUserSubRepo) ResetWeeklyUsage(_ context.Context, _ int64, _ *time.Time, resetAt time.Time) error {
	r.resetCalled = true
	r.resetAt = resetAt
	return nil
}

func TestAssignSubscriptionInitializesUsageWindows(t *testing.T) {
	groupRepo := &subscriptionGroupRepoStub{
		group: &Group{ID: 1, SubscriptionType: SubscriptionTypeSubscription},
	}
	subRepo := newSubscriptionUserSubRepoStub()
	svc := NewSubscriptionService(groupRepo, subRepo, nil, nil, nil)

	sub, err := svc.AssignSubscription(context.Background(), &AssignSubscriptionInput{
		UserID:       31,
		GroupID:      1,
		ValidityDays: 30,
		Notes:        "invite-trial",
	})

	require.NoError(t, err)
	require.Equal(t, 1, subRepo.createCalls)
	require.False(t, sub.StartsAt.IsZero())
	require.NotNil(t, sub.DailyWindowStart)
	require.Equal(t, timezone.StartOfDay(sub.StartsAt), *sub.DailyWindowStart)
	require.NotNil(t, sub.WeeklyWindowStart)
	require.Equal(t, sub.StartsAt, *sub.WeeklyWindowStart)
	require.NotNil(t, sub.MonthlyWindowStart)
	require.Equal(t, sub.StartsAt, *sub.MonthlyWindowStart)
}

type usableSubRepoStub struct {
	userSubRepoNoop
	sub *UserSubscription
	err error
}

func (r usableSubRepoStub) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*UserSubscription, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func TestSubscriptionGroupUsable_WeeklyLimitExceeded(t *testing.T) {
	startsAt := time.Date(2026, 7, 24, 4, 31, 4, 0, time.UTC)
	lateAnchor := time.Date(2026, 8, 18, 6, 22, 6, 0, time.UTC)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	limit := 50.0
	sub := &UserSubscription{
		Status:            SubscriptionStatusActive,
		UserID:            31,
		GroupID:           22,
		StartsAt:          startsAt,
		ExpiresAt:         startsAt.Add(30 * 24 * time.Hour),
		WeeklyWindowStart: &lateAnchor,
		WeeklyUsageUSD:    50.06,
	}
	svc := NewSubscriptionService(groupRepoNoop{}, usableSubRepoStub{sub: sub}, nil, nil, nil)
	svc.now = func() time.Time { return now }
	group := &Group{ID: 22, SubscriptionType: SubscriptionTypeSubscription, WeeklyLimitUSD: &limit}

	require.False(t, svc.SubscriptionGroupUsable(context.Background(), 31, group))
}

func TestSubscriptionGroupUsable_Expired(t *testing.T) {
	startsAt := time.Date(2026, 7, 24, 4, 31, 4, 0, time.UTC)
	now := time.Date(2026, 8, 23, 12, 31, 4, 0, time.UTC)
	sub := &UserSubscription{
		Status:    SubscriptionStatusActive,
		UserID:    31,
		GroupID:   22,
		StartsAt:  startsAt,
		ExpiresAt: startsAt.Add(30 * 24 * time.Hour),
	}
	svc := NewSubscriptionService(groupRepoNoop{}, usableSubRepoStub{sub: sub}, nil, nil, nil)
	svc.now = func() time.Time { return now }
	group := &Group{ID: 22, SubscriptionType: SubscriptionTypeSubscription}

	require.False(t, svc.SubscriptionGroupUsable(context.Background(), 31, group))
}

func TestSubscriptionGroupUsable_MissingSubscription(t *testing.T) {
	svc := NewSubscriptionService(groupRepoNoop{}, usableSubRepoStub{err: ErrSubscriptionNotFound}, nil, nil, nil)
	group := &Group{ID: 22, SubscriptionType: SubscriptionTypeSubscription}

	require.False(t, svc.SubscriptionGroupUsable(context.Background(), 31, group))
}

func TestSubscriptionGroupUsable_ActiveUnderLimit(t *testing.T) {
	startsAt := time.Date(2026, 7, 24, 4, 31, 4, 0, time.UTC)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	limit := 50.0
	window := startsAt.Add(28 * 24 * time.Hour)
	sub := &UserSubscription{
		Status:            SubscriptionStatusActive,
		UserID:            31,
		GroupID:           22,
		StartsAt:          startsAt,
		ExpiresAt:         startsAt.Add(30 * 24 * time.Hour),
		WeeklyWindowStart: &window,
		WeeklyUsageUSD:    10,
	}
	svc := NewSubscriptionService(groupRepoNoop{}, usableSubRepoStub{sub: sub}, nil, nil, nil)
	svc.now = func() time.Time { return now }
	group := &Group{ID: 22, SubscriptionType: SubscriptionTypeSubscription, WeeklyLimitUSD: &limit}

	require.True(t, svc.SubscriptionGroupUsable(context.Background(), 31, group))
}
