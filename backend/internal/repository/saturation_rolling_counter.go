package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// saturationRollingIncrScript records one timestamped capacity event and drops
// members that have left the rolling window. Shared by Anthropic / OpenAI /
// Antigravity saturation counters so window semantics cannot drift.
//
// KEYS[1] = event ZSET, ARGV[1] = windowSec. Returns: in-window count.
var saturationRollingIncrScript = redis.NewScript(`
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

func rollingSaturationCutoffMS(ctx context.Context, rdb *redis.Client, windowSeconds int) (int64, error) {
	if windowSeconds <= 0 {
		return 0, fmt.Errorf("invalid window: %d", windowSeconds)
	}
	now, err := rdb.Time(ctx).Result()
	if err != nil {
		return 0, err
	}
	return now.Add(-time.Duration(windowSeconds) * time.Second).UnixMilli(), nil
}

func zcountRollingSaturation(ctx context.Context, rdb *redis.Client, keys []string, cutoffMS int64) ([]int64, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	pipe := rdb.Pipeline()
	cmds := make([]*redis.IntCmd, len(keys))
	min := "(" + strconv.FormatInt(cutoffMS, 10)
	for i, key := range keys {
		cmds[i] = pipe.ZCount(ctx, key, min, "+inf")
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	out := make([]int64, len(keys))
	for i, cmd := range cmds {
		n, err := cmd.Result()
		if err != nil {
			continue
		}
		out[i] = n
	}
	return out, nil
}
