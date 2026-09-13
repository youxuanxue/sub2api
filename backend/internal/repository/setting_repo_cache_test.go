package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSettingRepository_CacheBasic(t *testing.T) {
	repo := &settingRepository{
		cache: make(map[string]settingCacheEntry),
		ttl:   50 * time.Millisecond,
	}

	ctx := context.Background()

	// 1. Initially empty cache, simulate setting loaded manually
	now := time.Now()
	repo.cache["site_name"] = settingCacheEntry{
		setting: &service.Setting{Key: "site_name", Value: "TokenKey", UpdatedAt: now},
		expires: now.Add(50 * time.Millisecond),
	}

	// 2. Read through Get
	s, err := repo.Get(ctx, "site_name")
	require.NoError(t, err)
	require.Equal(t, "TokenKey", s.Value)

	// 3. Read through GetValue
	val, err := repo.GetValue(ctx, "site_name")
	require.NoError(t, err)
	require.Equal(t, "TokenKey", val)

	// 4. Negative cache test
	repo.cache["missing_key"] = settingCacheEntry{
		setting: nil,
		expires: now.Add(50 * time.Millisecond),
	}
	_, err = repo.Get(ctx, "missing_key")
	require.ErrorIs(t, err, service.ErrSettingNotFound)

	// 5. GetMultiple from cache
	multi, err := repo.GetMultiple(ctx, []string{"site_name", "missing_key"})
	require.NoError(t, err)
	require.Equal(t, "TokenKey", multi["site_name"])
	require.NotContains(t, multi, "missing_key")

	// 6. GetAll from cache
	repo.allCached = true
	repo.allExpire = now.Add(50 * time.Millisecond)
	all, err := repo.GetAll(ctx)
	require.NoError(t, err)
	require.Equal(t, "TokenKey", all["site_name"])

	// 7. Expired cache
	time.Sleep(60 * time.Millisecond)
	repo.mu.RLock()
	entry := repo.cache["site_name"]
	require.True(t, time.Now().After(entry.expires))
	repo.mu.RUnlock()
}
