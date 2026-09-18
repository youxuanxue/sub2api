package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const anthropicSaturationCountPrefix = "anthropic_saturation_count:account:"

// anthropicSaturationIncrScript atomically records one timestamped event and
// removes events that have left the rolling window.
//
// KEYS[1] = event ZSET, ARGV[1] = windowSec. Returns: in-window count.
var anthropicSaturationIncrScript = redis.NewScript(`
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

type anthropicSaturationCounterCache struct {
	rdb *redis.Client
}

// NewAnthropicSaturationCounterCache builds the Redis-backed saturation counter.
func NewAnthropicSaturationCounterCache(rdb *redis.Client) service.AnthropicSaturationCounterCache {
	return &anthropicSaturationCounterCache{rdb: rdb}
}

func anthropicSaturationKey(accountID int64) string {
	return fmt.Sprintf("%s%d", anthropicSaturationCountPrefix, accountID)
}

func (c *anthropicSaturationCounterCache) IncrementSaturation(ctx context.Context, accountID int64, windowSeconds int) (int64, error) {
	if windowSeconds <= 0 {
		return 0, fmt.Errorf("invalid window: %d", windowSeconds)
	}
	key := anthropicSaturationKey(accountID)
	count, err := anthropicSaturationIncrScript.Run(ctx, c.rdb, []string{key}, windowSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment anthropic saturation: %w", err)
	}
	return count, nil
}

func (c *anthropicSaturationCounterCache) GetSaturationBatch(ctx context.Context, accountIDs []int64, windowSeconds int) (map[int64]int64, error) {
	out := make(map[int64]int64, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	if windowSeconds <= 0 {
		return nil, fmt.Errorf("invalid window: %d", windowSeconds)
	}
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("get anthropic saturation time: %w", err)
	}
	cutoff := now.Add(-time.Duration(windowSeconds) * time.Second).UnixMilli()
	keys := make([]string, len(accountIDs))
	for i, id := range accountIDs {
		keys[i] = anthropicSaturationKey(id)
	}
	pipe := c.rdb.Pipeline()
	cmds := make([]*redis.IntCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.ZCount(ctx, key, "("+fmt.Sprint(cutoff), "+inf")
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("zcount anthropic saturation: %w", err)
	}
	for i, cmd := range cmds {
		n, err := cmd.Result()
		if err != nil {
			continue
		}
		if n != 0 {
			out[accountIDs[i]] = n
		}
	}
	return out, nil
}
