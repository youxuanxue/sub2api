package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const oauth401AfterRefreshCounterPrefix = "oauth_401_after_refresh:account:"

// oauth401AfterRefreshIncrScript 原子地用 token 版本戳做闸门计数：
//   - Hash 字段 ver = 上一次 401 记录的 token 版本；count = 「换新 token 仍 401」累计次数。
//   - 无 baseline（key 不存在或已过 TTL）→ 种下 ver、count=0、设 TTL，返回 0。
//   - ver 递增（其间发生过成功 refresh）→ count+1、更新 ver、刷新 TTL，返回新 count。
//   - ver 相等（同 token / 并发突发）→ 仅刷新 TTL，返回当前 count。
//   - ver 变小（请求快照过旧）→ 不动，返回当前 count。
var oauth401AfterRefreshIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local ver = tonumber(ARGV[1])
	local ttl = tonumber(ARGV[2])

	local stored = redis.call('HGET', key, 'ver')
	if stored == false then
		redis.call('HSET', key, 'ver', ver, 'count', 0)
		redis.call('EXPIRE', key, ttl)
		return 0
	end

	stored = tonumber(stored)
	local count = tonumber(redis.call('HGET', key, 'count') or '0')
	if ver > stored then
		count = count + 1
		redis.call('HSET', key, 'ver', ver, 'count', count)
		redis.call('EXPIRE', key, ttl)
	elseif ver == stored then
		redis.call('EXPIRE', key, ttl)
	end

	return count
`)

type oauth401AfterRefreshCounterCache struct {
	rdb *redis.Client
}

// NewOAuth401AfterRefreshCounterCache 构造基于 Redis 的「refresh 后仍 401」计数器。
func NewOAuth401AfterRefreshCounterCache(rdb *redis.Client) service.OAuth401AfterRefreshCounterCache {
	return &oauth401AfterRefreshCounterCache{rdb: rdb}
}

func (c *oauth401AfterRefreshCounterCache) RecordOAuth401AfterRefresh(ctx context.Context, accountID int64, tokenVersion int64, windowMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", oauth401AfterRefreshCounterPrefix, accountID)

	ttlSeconds := windowMinutes * 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}

	result, err := oauth401AfterRefreshIncrScript.Run(ctx, c.rdb, []string{key}, tokenVersion, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("record oauth 401 after refresh: %w", err)
	}
	return result, nil
}

func (c *oauth401AfterRefreshCounterCache) ResetOAuth401AfterRefresh(ctx context.Context, accountID int64) error {
	key := fmt.Sprintf("%s%d", oauth401AfterRefreshCounterPrefix, accountID)
	return c.rdb.Del(ctx, key).Err()
}
