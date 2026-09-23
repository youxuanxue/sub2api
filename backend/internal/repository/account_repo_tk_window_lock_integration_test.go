//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNewAPIWindowLockSQLMatchesSchedulingAndCapacity(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil, nil)
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	group := mustCreateGroup(t, client, &service.Group{Name: "window-lock-parity"})
	var want, blocked []int64
	for _, tc := range []struct {
		name, platform string
		extra          map[string]any
		allowed        bool
	}{
		{"missing-marker", service.PlatformNewAPI, map[string]any{"newapi_weekly_reset": float64(reset.Unix())}, true},
		{"zero-marker", service.PlatformNewAPI, map[string]any{"newapi_weekly_reset": float64(reset.Unix()), "newapi_account_window_lock": 0.0}, true},
		{"null-marker", service.PlatformNewAPI, map[string]any{"newapi_monthly_reset": float64(reset.Unix()), "newapi_account_window_lock": nil}, true},
		{"scientific-reset", service.PlatformNewAPI, map[string]any{"newapi_weekly_reset": fmt.Sprintf(" %.9e ", float64(reset.Unix()))}, true},
		{"marked", service.PlatformNewAPI, map[string]any{"newapi_weekly_reset": float64(reset.Unix()), "newapi_account_window_lock": 1.0}, false},
		{"unrelated", service.PlatformNewAPI, map[string]any{"newapi_weekly_reset": float64(reset.Add(-time.Minute).Unix())}, false},
		{"invalid-reset", service.PlatformNewAPI, map[string]any{"newapi_weekly_reset": "bad"}, false},
		{"oversized-reset", service.PlatformNewAPI, map[string]any{"newapi_weekly_reset": "1e9999"}, false},
		{"other-platform", service.PlatformOpenAI, map[string]any{"newapi_weekly_reset": float64(reset.Unix())}, false},
	} {
		a := mustCreateAccount(t, client, &service.Account{Name: tc.name, Platform: tc.platform, RateLimitResetAt: &reset, Extra: tc.extra, Schedulable: true})
		mustBindAccountToGroup(t, client, a.ID, group.ID, 1)
		require.Equal(t, tc.allowed, a.IsSchedulable(), tc.name)
		if tc.allowed {
			want = append(want, a.ID)
		} else if tc.platform == service.PlatformNewAPI {
			blocked = append(blocked, a.ID)
		}
	}
	grouped, err := repo.ListSchedulableByGroupID(ctx, group.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, want, idsOfAccounts(grouped))
	all, err := repo.ListSchedulable(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, want, idsOfAccounts(all))
	platform, err := repo.ListSchedulableByPlatform(ctx, service.PlatformNewAPI)
	require.NoError(t, err)
	require.ElementsMatch(t, want, idsOfAccounts(platform))
	platforms, err := repo.ListSchedulableByPlatforms(ctx, []string{service.PlatformNewAPI})
	require.NoError(t, err)
	require.ElementsMatch(t, want, idsOfAccounts(platforms))
	batch, err := repo.ListSchedulableByGroupIDs(ctx, []int64{group.ID})
	require.NoError(t, err)
	require.ElementsMatch(t, want, idsOfAccounts(batch[group.ID]))
	loads, err := repo.ListSchedulableAccountLoads(ctx)
	require.NoError(t, err)
	var loadIDs []int64
	for _, a := range loads {
		loadIDs = append(loadIDs, a.ID)
	}
	require.ElementsMatch(t, want, loadIDs)
	active, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, service.PlatformNewAPI, "", service.StatusActive, "", group.ID, "", 0)
	require.NoError(t, err)
	require.ElementsMatch(t, want, idsOfAccounts(active))
	limited, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, service.PlatformNewAPI, "", "rate_limited", "", group.ID, "", 0)
	require.NoError(t, err)
	require.ElementsMatch(t, blocked, idsOfAccounts(limited))
	for _, id := range want {
		require.NoError(t, repo.BindGroups(ctx, id, nil))
	}
	ungrouped, err := repo.ListSchedulableUngroupedByPlatform(ctx, service.PlatformNewAPI)
	require.NoError(t, err)
	require.ElementsMatch(t, want, idsOfAccounts(ungrouped))
	ungroupedAll, err := repo.ListSchedulableUngroupedByPlatforms(ctx, []string{service.PlatformNewAPI})
	require.NoError(t, err)
	require.ElementsMatch(t, want, idsOfAccounts(ungroupedAll))
}

func TestModelRateLimitCascadeSQLMatchesSchedulingAndCapacity(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil, nil)
	now := time.Now().UTC().Truncate(time.Second)
	group := mustCreateGroup(t, client, &service.Group{Name: "model-cascade-parity"})

	mkLimits := func(count int) map[string]any {
		limits := make(map[string]any, count)
		for i := 0; i < count; i++ {
			limits[fmt.Sprintf("model-%d", i+1)] = map[string]any{
				"rate_limited_at":     now.Add(-time.Hour).Format(time.RFC3339),
				"rate_limit_reset_at": now.Add(time.Duration(i+1) * time.Hour).Format(time.RFC3339),
			}
		}
		return map[string]any{"model_rate_limits": limits}
	}

	under := mustCreateAccount(t, client, &service.Account{
		Name: "cascade-under", Platform: service.PlatformNewAPI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Extra: mkLimits(service.AccountWideModelRateLimitThreshold),
	})
	over := mustCreateAccount(t, client, &service.Account{
		Name: "cascade-over", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Extra: mkLimits(service.AccountWideModelRateLimitThreshold + 1),
	})
	mustBindAccountToGroup(t, client, under.ID, group.ID, 1)
	mustBindAccountToGroup(t, client, over.ID, group.ID, 2)

	require.True(t, under.IsSchedulable())
	require.False(t, under.IsRateLimited())
	require.False(t, over.IsSchedulable())
	require.True(t, over.IsRateLimited())

	grouped, err := repo.ListSchedulableByGroupID(ctx, group.ID)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{under.ID}, idsOfAccounts(grouped))

	all, err := repo.ListSchedulable(ctx)
	require.NoError(t, err)
	require.Contains(t, idsOfAccounts(all), under.ID)
	require.NotContains(t, idsOfAccounts(all), over.ID)

	platform, err := repo.ListSchedulableByPlatform(ctx, service.PlatformAnthropic)
	require.NoError(t, err)
	require.NotContains(t, idsOfAccounts(platform), over.ID)

	batch, err := repo.ListSchedulableByGroupIDs(ctx, []int64{group.ID})
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{under.ID}, idsOfAccounts(batch[group.ID]))

	capacity, err := repo.ListSchedulableCapacityByGroupIDs(ctx, []int64{group.ID})
	require.NoError(t, err)
	capacityIDs := make([]int64, 0, len(capacity))
	for _, row := range capacity {
		if row.GroupID == group.ID {
			capacityIDs = append(capacityIDs, row.AccountID)
		}
	}
	require.ElementsMatch(t, []int64{under.ID}, capacityIDs)

	active, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, "", "", service.StatusActive, "", group.ID, "", 0)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{under.ID}, idsOfAccounts(active))

	limited, _, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 100}, "", "", "rate_limited", "", group.ID, "", 0)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{over.ID}, idsOfAccounts(limited))
}
