//go:build integration

package repository

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheSnapshotUsesSlimMetadataButKeepsFullAccount(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)

	bucket := service.SchedulerBucket{GroupID: 2, Platform: service.PlatformGemini, Mode: service.SchedulerModeSingle}
	now := time.Now().UTC().Truncate(time.Second)
	limitReset := now.Add(10 * time.Minute)
	overloadUntil := now.Add(2 * time.Minute)
	tempUnschedUntil := now.Add(3 * time.Minute)
	windowEnd := now.Add(5 * time.Hour)

	account := service.Account{
		ID:          101,
		Name:        "gemini-heavy",
		Platform:    service.PlatformGemini,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Priority:    7,
		LastUsedAt:  &now,
		Credentials: map[string]any{
			"api_key":       "gemini-api-key",
			"access_token":  "secret-access-token",
			"project_id":    "proj-1",
			"oauth_type":    "ai_studio",
			"model_mapping": map[string]any{"gemini-2.5-pro": "gemini-2.5-pro"},
			"huge_blob":     strings.Repeat("x", 4096),
		},
		Extra: map[string]any{
			"mixed_scheduling":             true,
			"max_sessions":                 4,
			"session_idle_timeout_minutes": 11,
			"base_rpm":                     20,
			"rpm_strategy":                 "sticky_exempt",
			"rpm_sticky_buffer":            9,
			"user_msg_queue_mode":          "throttle",
			"user_msg_queue_enabled":       true,
			"session_id_masking_enabled":   true,
			"unused_large_field":           strings.Repeat("y", 4096),
		},
		RateLimitResetAt:       &limitReset,
		OverloadUntil:          &overloadUntil,
		TempUnschedulableUntil: &tempUnschedUntil,
		SessionWindowStart:     &now,
		SessionWindowEnd:       &windowEnd,
		SessionWindowStatus:    "active",
		GroupIDs:               []int64{bucket.GroupID},
		AccountGroups: []service.AccountGroup{
			{
				AccountID: 101,
				GroupID:   bucket.GroupID,
				Priority:  5,
				Group:     &service.Group{ID: bucket.GroupID, Name: "gemini-group"},
			},
		},
	}

	require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{account}))

	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)

	got := snapshot[0]
	require.NotNil(t, got)
	require.Equal(t, "gemini-api-key", got.GetCredential("api_key"))
	require.Equal(t, "proj-1", got.GetCredential("project_id"))
	require.Equal(t, "ai_studio", got.GetCredential("oauth_type"))
	require.NotEmpty(t, got.GetModelMapping())
	require.Empty(t, got.GetCredential("access_token"))
	require.Empty(t, got.GetCredential("huge_blob"))
	require.Equal(t, true, got.Extra["mixed_scheduling"])
	require.Equal(t, 4, got.GetMaxSessions())
	require.Equal(t, 11, got.GetSessionIdleTimeoutMinutes())
	require.Equal(t, 20, got.GetBaseRPM())
	require.Equal(t, "sticky_exempt", got.GetRPMStrategy())
	require.Equal(t, 9, got.GetRPMStickyBuffer())
	require.Equal(t, "throttle", got.GetUserMsgQueueMode())
	require.Equal(t, true, got.Extra["session_id_masking_enabled"])
	require.Nil(t, got.Extra["unused_large_field"])
	require.Equal(t, []int64{bucket.GroupID}, got.GroupIDs)
	require.Len(t, got.AccountGroups, 1)
	require.Equal(t, account.ID, got.AccountGroups[0].AccountID)
	require.Equal(t, bucket.GroupID, got.AccountGroups[0].GroupID)
	require.Nil(t, got.AccountGroups[0].Group)

	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Equal(t, "secret-access-token", full.GetCredential("access_token"))
	require.Equal(t, strings.Repeat("x", 4096), full.GetCredential("huge_blob"))
	require.Len(t, full.AccountGroups, 1)
	require.NotNil(t, full.AccountGroups[0].Group)
}

// TestSchedulerCacheUpdateLastUsedOnlyTouchesMetaKey 回归保护 upstream
// Wei-Shaw/sub2api#1723：UpdateLastUsed 只应刷新调度热路径读取的 slim meta
// 键（sched:meta:<id>），绝不重写完整账号键（sched:acc:<id>）。重写完整账号
// JSON（实测 3-12 KB）仅为 bump 一个时间戳，会造成巨大的 Redis 读写放大。
func TestSchedulerCacheUpdateLastUsedOnlyTouchesMetaKey(t *testing.T) {
	ctx := context.Background()
	rdb := testRedis(t)
	cache := NewSchedulerCache(rdb)

	bucket := service.SchedulerBucket{GroupID: 3, Platform: service.PlatformAnthropic, Mode: service.SchedulerModeSingle}
	t0 := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	t1 := t0.Add(time.Hour)

	account := service.Account{
		ID:          202,
		Name:        "claude-lru",
		Platform:    service.PlatformAnthropic,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 2,
		Priority:    1,
		LastUsedAt:  &t0,
		Credentials: map[string]any{
			"api_key":      "anthropic-api-key",
			"access_token": "secret-access-token",
			"huge_blob":    strings.Repeat("x", 8192),
		},
		Extra: map[string]any{
			"base_rpm":         28,
			"max_sessions":     30,
			"mixed_scheduling": true,
		},
		GroupIDs: []int64{bucket.GroupID},
		AccountGroups: []service.AccountGroup{
			{AccountID: 202, GroupID: bucket.GroupID, Priority: 1},
		},
	}

	require.NoError(t, cache.SetSnapshot(ctx, bucket, []service.Account{account}))

	fullKey := schedulerAccountKey(strconv.FormatInt(account.ID, 10))
	rawBefore, err := rdb.Get(ctx, fullKey).Result()
	require.NoError(t, err)
	require.NotEmpty(t, rawBefore)

	// 触发 LastUsedAt bump（调度热路径每个 flush 周期都会走到这里）。
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: t1}))

	// 核心断言：完整账号键的字节必须完全不变 —— 没有写放大。
	rawAfter, err := rdb.Get(ctx, fullKey).Result()
	require.NoError(t, err)
	require.Equal(t, rawBefore, rawAfter,
		"UpdateLastUsed 不得重写完整账号键 sched:acc:<id>（#1723 写放大回归）")

	// 调度 LRU 读取的 meta 键必须反映新的 LastUsedAt。
	snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, snapshot, 1)
	require.NotNil(t, snapshot[0].LastUsedAt)
	require.True(t, snapshot[0].LastUsedAt.Equal(t1),
		"meta 键的 LastUsedAt 应被刷新为最新值，供 LRU tiebreak 使用")

	// 完整账号键仍可 hydrate 出完整凭据；其 LastUsedAt 保持旧值是被接受的
	// 契约——它不参与 LRU，会在下次快照重建/账号变更时刷新。
	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.Equal(t, "secret-access-token", full.GetCredential("access_token"))
	require.Equal(t, strings.Repeat("x", 8192), full.GetCredential("huge_blob"))
	require.NotNil(t, full.LastUsedAt)
	require.True(t, full.LastUsedAt.Equal(t0),
		"完整账号键的 LastUsedAt 在 UpdateLastUsed 后保持旧值（不重写即不刷新，契约如此）")
}
