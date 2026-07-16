//go:build unit

package repository

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newSchedulerCacheUnit(t *testing.T) *schedulerCache {
	cache, _ := newSchedulerCacheUnitWithRedis(t)
	return cache
}

func newSchedulerCacheUnitWithRedis(t *testing.T) (*schedulerCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache, ok := newSchedulerCacheWithChunkSizes(rdb, defaultSchedulerSnapshotMGetChunkSize, defaultSchedulerSnapshotWriteChunkSize).(*schedulerCache)
	require.True(t, ok)
	return cache, mr
}

func TestSchedulerCacheWriteAccountsSkipsUnencodableTimes(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)

	cacheable, err := cache.writeAccounts(ctx, []service.Account{
		{ID: 111, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey},
		{ID: 112, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, ExpiresAt: &invalidTime},
	})
	require.NoError(t, err)
	require.Len(t, cacheable, 1)
	require.Equal(t, int64(111), cacheable[0].ID)

	cached, err := cache.GetAccount(ctx, 111)
	require.NoError(t, err)
	require.NotNil(t, cached)

	invalid, err := cache.GetAccount(ctx, 112)
	require.NoError(t, err)
	require.Nil(t, invalid)
}

func TestSchedulerCacheSetAccountClearsUnencodablePayload(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)

	account := service.Account{ID: 113, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
	require.NoError(t, cache.SetAccount(ctx, &account))

	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	account.ExpiresAt = &invalidTime
	require.NoError(t, cache.SetAccount(ctx, &account))

	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, cached)
}

func TestSchedulerCacheUpdateLastUsedClearsUnencodablePayload(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 114, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
	require.NoError(t, cache.SetAccount(ctx, &account))

	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, cache.UpdateLastUsed(ctx, map[int64]time.Time{account.ID: invalidTime}))

	cached, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, cached)
}

func TestSchedulerCacheSnapshotAccountIDReusePreservesPayloadAndMembers(t *testing.T) {
	ctx := context.Background()
	cache, _ := newSchedulerCacheUnitWithRedis(t)
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	validOne := service.Account{
		ID:          701,
		Name:        "first",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Credentials: map[string]any{"model_mapping": map[string]any{"z": "last", "a": "first"}},
		Extra:       map[string]any{"mixed_scheduling": true},
		GroupIDs:    []int64{17},
	}
	validTwo := service.Account{ID: 702, Name: "second", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
	invalid := service.Account{ID: 799, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, ExpiresAt: &invalidTime}
	accounts := []service.Account{validOne, invalid, validTwo, validOne}

	single := service.SchedulerBucket{GroupID: 17, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	singleToken, err := cache.CaptureBucketWriteToken(ctx, single)
	require.NoError(t, err)
	accountIDs, err := cache.SetSnapshotAndReturnAccountIDs(ctx, single, singleToken, accounts)
	require.NoError(t, err)
	require.Equal(t, []int64{701, 702, 701}, accountIDs, "应保留可编码账号的原顺序和重复项")

	wantFull, err := json.Marshal(validOne)
	require.NoError(t, err)
	wantMeta, err := json.Marshal(buildSchedulerMetadataAccount(validOne))
	require.NoError(t, err)
	fullBefore, err := cache.rdb.Get(ctx, schedulerAccountKey("701")).Bytes()
	require.NoError(t, err)
	metaBefore, err := cache.rdb.Get(ctx, schedulerAccountMetaKey("701")).Bytes()
	require.NoError(t, err)
	require.Equal(t, wantFull, fullBefore)
	require.Equal(t, wantMeta, metaBefore)

	forced := service.SchedulerBucket{GroupID: 17, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeForced}
	forcedToken, err := cache.CaptureBucketWriteToken(ctx, forced)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshotByAccountIDs(ctx, forced, forcedToken, accountIDs))

	fullAfter, err := cache.rdb.Get(ctx, schedulerAccountKey("701")).Bytes()
	require.NoError(t, err)
	metaAfter, err := cache.rdb.Get(ctx, schedulerAccountMetaKey("701")).Bytes()
	require.NoError(t, err)
	require.Equal(t, fullBefore, fullAfter, "ID-only 路径不得重写完整账号键")
	require.Equal(t, metaBefore, metaAfter, "ID-only 路径不得重写调度元数据键")

	for _, bucket := range []service.SchedulerBucket{single, forced} {
		version, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerActivePrefix, bucket)).Result()
		require.NoError(t, err)
		members, err := cache.rdb.ZRange(ctx, schedulerSnapshotKey(bucket, version), 0, -1).Result()
		require.NoError(t, err)
		require.Equal(t, []string{"702", "701"}, members, bucket.String())
	}
	missing, err := cache.GetAccount(ctx, invalid.ID)
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestSchedulerCacheSnapshotAccountIDReuseKeepsEmptySnapshotSemantics(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	invalidTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	accounts := []service.Account{{ID: 811, Platform: service.PlatformOpenAI, ExpiresAt: &invalidTime}}

	single := service.SchedulerBucket{GroupID: 18, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	singleToken, err := cache.CaptureBucketWriteToken(ctx, single)
	require.NoError(t, err)
	accountIDs, err := cache.SetSnapshotAndReturnAccountIDs(ctx, single, singleToken, accounts)
	require.NoError(t, err)
	require.Empty(t, accountIDs)

	forced := service.SchedulerBucket{GroupID: 18, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeForced}
	forcedToken, err := cache.CaptureBucketWriteToken(ctx, forced)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshotByAccountIDs(ctx, forced, forcedToken, accountIDs))

	for _, bucket := range []service.SchedulerBucket{single, forced} {
		ready, err := cache.rdb.Get(ctx, schedulerBucketKey(schedulerReadyPrefix, bucket)).Result()
		require.NoError(t, err)
		require.Equal(t, "1", ready)
		snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.False(t, hit, bucket.String())
		require.Nil(t, snapshot)
	}
}

func TestSchedulerCacheSetSnapshotByAccountIDsKeepsFencing(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	bucket := service.SchedulerBucket{GroupID: 19, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeForced}

	err := cache.SetSnapshotByAccountIDs(ctx, bucket, service.SchedulerBucketWriteToken{}, []int64{901})
	require.ErrorIs(t, err, service.ErrSchedulerBucketWriteFenced)
	_, err = cache.rdb.Get(ctx, schedulerBucketKey(schedulerVersionPrefix, bucket)).Result()
	require.ErrorIs(t, err, redis.Nil)

	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.RetireBucket(ctx, bucket))
	err = cache.SetSnapshotByAccountIDs(ctx, bucket, token, []int64{901})
	require.ErrorIs(t, err, service.ErrSchedulerBucketRetired)
}

func TestSchedulerCacheSetSnapshotByAccountIDsDoesNotResurrectDeletedAccount(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 902, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	single := service.SchedulerBucket{GroupID: 20, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	singleToken, err := cache.CaptureBucketWriteToken(ctx, single)
	require.NoError(t, err)
	accountIDs, err := cache.SetSnapshotAndReturnAccountIDs(ctx, single, singleToken, []service.Account{account})
	require.NoError(t, err)
	require.Equal(t, []int64{account.ID}, accountIDs)
	require.NoError(t, cache.DeleteAccount(ctx, account.ID))

	forced := service.SchedulerBucket{GroupID: 20, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeForced}
	forcedToken, err := cache.CaptureBucketWriteToken(ctx, forced)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshotByAccountIDs(ctx, forced, forcedToken, accountIDs))

	full, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, full, "ID-only 发布不得复活已删除的完整账号键")
	snapshot, hit, err := cache.GetSnapshot(ctx, forced)
	require.NoError(t, err)
	require.False(t, hit, "元数据缺失时必须安全回源，而不是返回残缺快照")
	require.Nil(t, snapshot)
}

func TestMarshalSchedulerCacheAccountKeepsEncodingJSONWireFormat(t *testing.T) {
	cases := []struct {
		name    string
		account service.Account
	}{
		{name: "nil collections", account: service.Account{ID: 801}},
		{name: "empty collections", account: service.Account{
			ID:          802,
			Credentials: map[string]any{},
			Extra:       map[string]any{},
			GroupIDs:    []int64{},
			Groups:      []*service.Group{},
		}},
		{name: "nested maps and escaping", account: service.Account{
			ID:          803,
			Credentials: map[string]any{"model_mapping": map[string]any{"z": "<last>", "a": "&first"}},
			Extra:       map[string]any{"mixed_scheduling": true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full, meta, err := marshalSchedulerCacheAccount(tc.account)
			require.NoError(t, err)
			wantFull, err := json.Marshal(tc.account)
			require.NoError(t, err)
			wantMeta, err := json.Marshal(buildSchedulerMetadataAccount(tc.account))
			require.NoError(t, err)
			require.Equal(t, wantFull, full)
			require.Equal(t, wantMeta, meta)
		})
	}
}

func TestBuildSchedulerMetadataAccount_KeepsOpenAIWSFlags(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_enabled": true,
			"openai_oauth_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
			"openai_ws_force_http":                         true,
			"openai_responses_mode":                        "force_chat_completions",
			"openai_responses_supported":                   false,
			"mixed_scheduling":                             true,
			"unused_large_field":                           "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, true, got.Extra["openai_oauth_responses_websockets_v2_enabled"])
	require.Equal(t, service.OpenAIWSIngressModePassthrough, got.Extra["openai_oauth_responses_websockets_v2_mode"])
	require.Equal(t, true, got.Extra["openai_ws_force_http"])
	require.Equal(t, "force_chat_completions", got.Extra["openai_responses_mode"])
	require.Equal(t, false, got.Extra["openai_responses_supported"])
	require.Equal(t, true, got.Extra["mixed_scheduling"])
	require.Nil(t, got.Extra["unused_large_field"])
}

// 回归保护：调度快照必须保留 privacy_mode 字段。
// 缺失会导致 Account.IsPrivacySet() 永远返回 false，
// 凡是开启 require_privacy_set 的分组都会卡住所有 OpenAI/Antigravity 账号
// （SetError("Privacy not set, required by group ...") 反复触发）。
func TestBuildSchedulerMetadataAccount_KeepsPrivacyMode(t *testing.T) {
	t.Run("openai_training_off", func(t *testing.T) {
		account := service.Account{
			ID:       1,
			Platform: service.PlatformOpenAI,
			Extra: map[string]any{
				"privacy_mode": service.PrivacyModeTrainingOff,
			},
		}

		got := buildSchedulerMetadataAccount(account)

		require.Equal(t, service.PrivacyModeTrainingOff, got.Extra["privacy_mode"])
		require.True(t, got.IsPrivacySet(),
			"meta account 必须能够通过 IsPrivacySet 检查；privacy_mode 被白名单过滤会触发 ‘Privacy not set’ 死循环")
	})

	t.Run("antigravity_privacy_set", func(t *testing.T) {
		account := service.Account{
			ID:       2,
			Platform: service.PlatformAntigravity,
			Extra: map[string]any{
				"privacy_mode": service.AntigravityPrivacySet,
			},
		}

		got := buildSchedulerMetadataAccount(account)

		require.Equal(t, service.AntigravityPrivacySet, got.Extra["privacy_mode"])
		require.True(t, got.IsPrivacySet())
	})

	t.Run("training_set_failed_remains_unset", func(t *testing.T) {
		account := service.Account{
			ID:       3,
			Platform: service.PlatformOpenAI,
			Extra: map[string]any{
				"privacy_mode": service.PrivacyModeFailed,
			},
		}

		got := buildSchedulerMetadataAccount(account)

		require.Equal(t, service.PrivacyModeFailed, got.Extra["privacy_mode"])
		require.False(t, got.IsPrivacySet(),
			"非 training_off 的 privacy_mode 仍应被识别为未设置，避免误放行")
	})
}

func TestBuildSchedulerMetadataAccount_KeepsSlimGroupMembership(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformAnthropic,
		GroupIDs: []int64{7, 9, 7, 0},
		AccountGroups: []service.AccountGroup{
			{
				AccountID: 42,
				GroupID:   7,
				Priority:  2,
				Account:   &service.Account{ID: 42, Name: "drop-from-metadata"},
				Group:     &service.Group{ID: 7, Name: "drop-from-metadata"},
			},
			{
				AccountID: 42,
				GroupID:   11,
				Priority:  3,
				Group:     &service.Group{ID: 11, Name: "drop-from-metadata"},
			},
			{
				AccountID: 42,
				GroupID:   0,
				Priority:  4,
			},
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, []int64{7, 9, 11}, got.GroupIDs)
	require.Len(t, got.AccountGroups, 2)
	require.Equal(t, int64(42), got.AccountGroups[0].AccountID)
	require.Equal(t, int64(7), got.AccountGroups[0].GroupID)
	require.Equal(t, 2, got.AccountGroups[0].Priority)
	require.Nil(t, got.AccountGroups[0].Account)
	require.Nil(t, got.AccountGroups[0].Group)
	require.Equal(t, int64(11), got.AccountGroups[1].GroupID)
	require.Nil(t, got.Groups)
}

func TestBuildSchedulerMetadataAccount_KeepsQuotaAutoPauseFields(t *testing.T) {
	account := service.Account{
		ID: 88,
		Extra: map[string]any{
			"codex_5h_used_percent":        12.34,
			"codex_7d_used_percent":        56.78,
			"codex_5h_reset_at":            "2026-05-29T10:00:00Z",
			"codex_7d_reset_at":            "2026-06-01T10:00:00Z",
			"codex_5h_reset_after_seconds": 300,
			"codex_7d_reset_after_seconds": 600,
			"codex_usage_updated_at":       "2026-05-29T09:00:00Z",
			"auto_pause_5h_threshold":      0.95,
			"auto_pause_7d_threshold":      0.96,
			"auto_pause_5h_disabled":       true,
			"auto_pause_7d_disabled":       false,
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, 12.34, got.Extra["codex_5h_used_percent"])
	require.Equal(t, 56.78, got.Extra["codex_7d_used_percent"])
	require.Equal(t, "2026-05-29T10:00:00Z", got.Extra["codex_5h_reset_at"])
	require.Equal(t, "2026-06-01T10:00:00Z", got.Extra["codex_7d_reset_at"])
	require.Equal(t, 300, got.Extra["codex_5h_reset_after_seconds"])
	require.Equal(t, 600, got.Extra["codex_7d_reset_after_seconds"])
	require.Equal(t, "2026-05-29T09:00:00Z", got.Extra["codex_usage_updated_at"])
	require.Equal(t, 0.95, got.Extra["auto_pause_5h_threshold"])
	require.Equal(t, 0.96, got.Extra["auto_pause_7d_threshold"])
	require.Equal(t, true, got.Extra["auto_pause_5h_disabled"])
	require.Equal(t, false, got.Extra["auto_pause_7d_disabled"])
}

func TestBuildSchedulerMetadataAccount_KeepsModelRateLimits(t *testing.T) {
	account := service.Account{
		ID:       90,
		Platform: service.PlatformAntigravity,
		Extra: map[string]any{
			"model_rate_limits": map[string]any{
				"gemini-3-flash": map[string]any{
					"rate_limit_reset_at": "2026-05-30T10:10:00Z",
				},
				"antigravity:gemini": map[string]any{
					"rate_limit_reset_at": "2026-05-30T10:10:00Z",
				},
			},
			"unused_large_field": "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	limits, ok := got.Extra["model_rate_limits"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, limits, "gemini-3-flash")
	require.Contains(t, limits, "antigravity:gemini")
	require.Nil(t, got.Extra["unused_large_field"])
}

func TestBuildSchedulerMetadataAccount_KeepsSparkShadowRoutingIdentity(t *testing.T) {
	parentID := int64(100)
	account := service.Account{
		ID:              200,
		Platform:        service.PlatformOpenAI,
		Type:            service.AccountTypeOAuth,
		ParentAccountID: &parentID,
		QuotaDimension:  service.QuotaDimensionSpark,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-5.3-codex-spark": "gpt-5.3-codex-spark",
			},
			"compact_model_mapping": map[string]any{
				"gpt-5.4": "gpt-5.4-openai-compact",
			},
			"access_token": "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.NotNil(t, got.ParentAccountID)
	require.Equal(t, parentID, *got.ParentAccountID)
	require.Equal(t, service.QuotaDimensionSpark, got.QuotaDimension)
	require.Equal(t, map[string]any{"gpt-5.3-codex-spark": "gpt-5.3-codex-spark"}, got.Credentials["model_mapping"])
	require.Equal(t, map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"}, got.Credentials["compact_model_mapping"])
	require.Nil(t, got.Credentials["access_token"])
}

func TestBuildSchedulerMetadataAccount_KeepsMirrorStubRoutingMetadata(t *testing.T) {
	account := service.Account{
		ID:       300,
		Name:     "kiro-us6",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":         "tk-edge",
			"base_url":        "https://api-us6.tokenkey.dev",
			"mirror_platform": "kiro",
			"pool_mode":       true,
			"access_token":    "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, "https://api-us6.tokenkey.dev", got.Credentials["base_url"])
	require.Equal(t, "kiro", got.Credentials["mirror_platform"])
	require.Equal(t, true, got.Credentials["pool_mode"])
	require.Equal(t, "tk-edge", got.Credentials["api_key"])
	require.Nil(t, got.Credentials["access_token"])
}

var schedulerCachePayloadBenchmarkSink int

func BenchmarkSchedulerCacheAccountPayloadReuse(b *testing.B) {
	for _, size := range []int{1, 100, 10_000} {
		accounts := schedulerCacheBenchmarkAccounts(size)
		b.Run(fmt.Sprintf("pair_baseline_%d_accounts", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				first, err := benchmarkSchedulerLegacySnapshotPayload(accounts)
				if err != nil {
					b.Fatal(err)
				}
				second, err := benchmarkSchedulerLegacySnapshotPayload(accounts)
				if err != nil {
					b.Fatal(err)
				}
				schedulerCachePayloadBenchmarkSink = first + second
			}
		})
		b.Run(fmt.Sprintf("pair_reuse_%d_accounts", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ids, total, err := benchmarkSchedulerReusableSnapshotPayload(accounts)
				if err != nil {
					b.Fatal(err)
				}
				// 第二个桶仍构造成员，只跳过账号 JSON 与全局账号键。
				total += len(schedulerSnapshotMembers(ids))
				schedulerCachePayloadBenchmarkSink = total
			}
		})
		b.Run(fmt.Sprintf("first_baseline_%d_accounts", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				total, err := benchmarkSchedulerLegacySnapshotPayload(accounts)
				if err != nil {
					b.Fatal(err)
				}
				schedulerCachePayloadBenchmarkSink = total
			}
		})
		b.Run(fmt.Sprintf("first_reuse_%d_accounts", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ids, total, err := benchmarkSchedulerReusableSnapshotPayload(accounts)
				if err != nil {
					b.Fatal(err)
				}
				total += len(ids)
				schedulerCachePayloadBenchmarkSink = total
			}
		})
	}
}

func benchmarkSchedulerLegacySnapshotPayload(accounts []service.Account) (int, error) {
	cacheable := make([]service.Account, 0, len(accounts))
	total := 0
	for _, account := range accounts {
		full, meta, err := marshalSchedulerCacheAccount(account)
		if err != nil {
			continue
		}
		total += len(full) + len(meta)
		cacheable = append(cacheable, account)
	}
	members := make([]redis.Z, 0, len(cacheable))
	for idx, account := range cacheable {
		members = append(members, redis.Z{Score: float64(idx), Member: strconv.FormatInt(account.ID, 10)})
	}
	return total + len(members), nil
}

func benchmarkSchedulerReusableSnapshotPayload(accounts []service.Account) ([]int64, int, error) {
	accountIDs := make([]int64, 0, len(accounts))
	total := 0
	for _, account := range accounts {
		full, meta, err := marshalSchedulerCacheAccount(account)
		if err != nil {
			continue
		}
		total += len(full) + len(meta)
		accountIDs = append(accountIDs, account.ID)
	}
	total += len(schedulerSnapshotMembers(accountIDs))
	return accountIDs, total, nil
}

func schedulerCacheBenchmarkAccounts(size int) []service.Account {
	largeValue := strings.Repeat("x", 4096)
	credentials := map[string]any{
		"api_key":       "benchmark-key",
		"model_mapping": map[string]any{"z-model": "z-target", "a-model": "a-target"},
		"large_value":   largeValue,
	}
	extra := map[string]any{
		"mixed_scheduling": true,
		"model_rate_limits": map[string]any{
			"z-model": map[string]any{"rate_limit_reset_at": "2026-07-16T00:00:00Z"},
			"a-model": map[string]any{"rate_limit_reset_at": "2026-07-16T00:00:00Z"},
		},
		"large_value": largeValue,
	}
	accounts := make([]service.Account, size)
	for i := range accounts {
		id := int64(i + 1)
		accounts[i] = service.Account{
			ID:          id,
			Name:        "benchmark-account",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Credentials: credentials,
			Extra:       extra,
			GroupIDs:    []int64{7, 9},
			AccountGroups: []service.AccountGroup{
				{AccountID: id, GroupID: 7, Priority: 1},
				{AccountID: id, GroupID: 9, Priority: 2},
			},
		}
	}
	return accounts
}
