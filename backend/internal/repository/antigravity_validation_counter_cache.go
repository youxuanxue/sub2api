package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const antigravityValidationCounterPrefix = "antigravity_validation_count:account:"

var antigravityValidationCounterIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local ttl = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, ttl)
	end

	return count
`)

type antigravityValidationCounterCache struct {
	rdb *redis.Client
}

// NewAntigravityValidationCounterCache 创建 Antigravity VALIDATION_REQUIRED
// 连续命中计数器缓存实例。
func NewAntigravityValidationCounterCache(rdb *redis.Client) service.AntigravityValidationCounterCache {
	return &antigravityValidationCounterCache{rdb: rdb}
}

// IncrementAntigravityValidationCount 原子递增计数并返回当前值。
func (c *antigravityValidationCounterCache) IncrementAntigravityValidationCount(ctx context.Context, accountID int64, windowMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", antigravityValidationCounterPrefix, accountID)

	ttlSeconds := windowMinutes * 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}

	result, err := antigravityValidationCounterIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment antigravity validation count: %w", err)
	}
	return result, nil
}

// ResetAntigravityValidationCount 清零计数器。
func (c *antigravityValidationCounterCache) ResetAntigravityValidationCount(ctx context.Context, accountID int64) error {
	key := fmt.Sprintf("%s%d", antigravityValidationCounterPrefix, accountID)
	return c.rdb.Del(ctx, key).Err()
}
