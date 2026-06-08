package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const oauth401AfterRefreshCounterPrefix = "oauth_401_after_refresh:account:"

// oauth401AfterRefreshIncrScript 原子地用 token 版本戳做闸门，维护两个互补计数维度，
// 返回 {count, sameCount}：
//   - Hash 字段 ver = 上次 401 记录的 token 版本；count = 「换新 token 仍 401」累计；
//     same = 「同一有效 token 跨冷却周期持续 401」累计；same_at = 上次 same 计数的服务端秒戳。
//   - 无 baseline（key 不存在或已过 TTL）→ 种下 ver/count=0/same=0/same_at=now、设 TTL，返回 {0,0}。
//   - ver 递增（其间发生过成功 refresh，新 token 仍 401）→ count+1、更新 ver、把 same 维度
//     重置到新版本 baseline（same=0、same_at=now）、刷新 TTL，返回 {count,0}。
//   - ver 相等（同 token，未刷新）→ 仅当 now-same_at >= debounce 才 same+1 并更新 same_at
//     （把一个冷却周期内的并发突发折叠成 1），否则只刷 TTL；返回 {count,same}。
//   - ver 变小（请求快照过旧）→ 不动，返回 {count,same}。
//
// 用服务端 redis.call('TIME') 取时间（同 concurrency_cache.go 房式），跨网关实例确定、
// 无客户端时钟漂移；TIME 需 effects replication，故首行 replicate_commands()（Redis 5+ 为 no-op）。
var oauth401AfterRefreshIncrScript = redis.NewScript(`
	redis.replicate_commands()
	local key = KEYS[1]
	local ver = tonumber(ARGV[1])
	local ttl = tonumber(ARGV[2])
	local debounce = tonumber(ARGV[3])
	local now = tonumber(redis.call('TIME')[1])

	local stored = redis.call('HGET', key, 'ver')
	if stored == false then
		redis.call('HSET', key, 'ver', ver, 'count', 0, 'same', 0, 'same_at', now)
		redis.call('EXPIRE', key, ttl)
		return {0, 0}
	end

	stored = tonumber(stored)
	local count = tonumber(redis.call('HGET', key, 'count') or '0')
	local same = tonumber(redis.call('HGET', key, 'same') or '0')
	if ver > stored then
		count = count + 1
		same = 0
		redis.call('HSET', key, 'ver', ver, 'count', count, 'same', 0, 'same_at', now)
		redis.call('EXPIRE', key, ttl)
	elseif ver == stored then
		local sameAt = tonumber(redis.call('HGET', key, 'same_at') or '0')
		if (now - sameAt) >= debounce then
			same = same + 1
			redis.call('HSET', key, 'same', same, 'same_at', now)
		end
		redis.call('EXPIRE', key, ttl)
	end

	return {count, same}
`)

type oauth401AfterRefreshCounterCache struct {
	rdb *redis.Client
}

// NewOAuth401AfterRefreshCounterCache 构造基于 Redis 的「refresh 后仍 401」计数器。
func NewOAuth401AfterRefreshCounterCache(rdb *redis.Client) service.OAuth401AfterRefreshCounterCache {
	return &oauth401AfterRefreshCounterCache{rdb: rdb}
}

func (c *oauth401AfterRefreshCounterCache) RecordOAuth401AfterRefresh(ctx context.Context, accountID int64, tokenVersion int64, windowMinutes, debounceSeconds int) (int64, int64, error) {
	key := fmt.Sprintf("%s%d", oauth401AfterRefreshCounterPrefix, accountID)

	ttlSeconds := windowMinutes * 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}
	if debounceSeconds < 0 {
		debounceSeconds = 0
	}

	result, err := oauth401AfterRefreshIncrScript.Run(ctx, c.rdb, []string{key}, tokenVersion, ttlSeconds, debounceSeconds).Int64Slice()
	if err != nil {
		return 0, 0, fmt.Errorf("record oauth 401 after refresh: %w", err)
	}
	// 脚本恒返回 {count, same}；防御性兜底，长度不足按 0 处理。
	var count, same int64
	if len(result) > 0 {
		count = result[0]
	}
	if len(result) > 1 {
		same = result[1]
	}
	return count, same, nil
}

func (c *oauth401AfterRefreshCounterCache) ResetOAuth401AfterRefresh(ctx context.Context, accountID int64) error {
	key := fmt.Sprintf("%s%d", oauth401AfterRefreshCounterPrefix, accountID)
	return c.rdb.Del(ctx, key).Err()
}
