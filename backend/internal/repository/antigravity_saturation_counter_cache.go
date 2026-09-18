package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const antigravitySaturationCountPrefix = "antigravity_saturation_count:account:"

func (c *antigravitySaturationCounterCache) GetSaturationBatch(ctx context.Context, scopes []service.AntigravitySaturationScope, windowSeconds int) (map[service.AntigravitySaturationScope]int64, error) {
	out := make(map[service.AntigravitySaturationScope]int64, len(scopes))
	if len(scopes) == 0 {
		return out, nil
	}
	if windowSeconds <= 0 {
		return nil, fmt.Errorf("invalid window: %d", windowSeconds)
	}
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("get antigravity saturation time: %w", err)
	}
	cutoff := now.Add(-time.Duration(windowSeconds) * time.Second).UnixMilli()
	keys := make([]string, len(scopes))
	for i, scope := range scopes {
		keys[i] = antigravitySaturationKey(scope.AccountID, scope.ModelKey)
	}
	pipe := c.rdb.Pipeline()
	cmds := make([]*redis.IntCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.ZCount(ctx, key, "("+fmt.Sprint(cutoff), "+inf")
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("zcount antigravity saturation: %w", err)
	}
	for i, cmd := range cmds {
		count, err := cmd.Result()
		if err != nil {
			continue
		}
		if count != 0 {
			out[scopes[i]] = count
		}
	}
	return out, nil
}

var antigravitySaturationIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local window = tonumber(ARGV[1])
	local now = redis.call('TIME')
	local now_ms = now[1] * 1000 + math.floor(now[2] / 1000)
	local cutoff = now_ms - window * 1000

	redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
	local count = redis.call('ZCARD', key)
	local member = tostring(now_ms) .. ':' .. tostring(count)
	redis.call('ZADD', key, now_ms, member)
	count = count + 1
	redis.call('PEXPIRE', key, window * 1000 + 1000)

	return count
`)

type antigravitySaturationCounterCache struct {
	rdb *redis.Client
}

func NewAntigravitySaturationCounterCache(rdb *redis.Client) service.AntigravitySaturationCounterCache {
	return &antigravitySaturationCounterCache{rdb: rdb}
}

func antigravitySaturationKey(accountID int64, modelKey string) string {
	return fmt.Sprintf("%s%d:model:%s", antigravitySaturationCountPrefix, accountID, modelKey)
}

func (c *antigravitySaturationCounterCache) IncrementSaturation(
	ctx context.Context,
	accountID int64,
	modelKey string,
	windowSeconds int,
) (int64, error) {
	modelKey = strings.TrimSpace(modelKey)
	if modelKey == "" {
		return 0, fmt.Errorf("model key is required")
	}
	if windowSeconds <= 0 {
		return 0, fmt.Errorf("invalid window: %d", windowSeconds)
	}
	count, err := antigravitySaturationIncrScript.Run(
		ctx,
		c.rdb,
		[]string{antigravitySaturationKey(accountID, modelKey)},
		windowSeconds,
	).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment antigravity saturation: %w", err)
	}
	return count, nil
}
