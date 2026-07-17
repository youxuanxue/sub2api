package repository

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const anthropicSaturationCountPrefix = "anthropic_saturation_count:account:"

// anthropicSaturationIncrScript atomically INCRs the per-account saturation
// counter; if it just became 1 (the key was absent/expired) it EXPIREs the key
// to windowSec. A sustained burst therefore keeps the ORIGINAL fixed window
// instead of sliding it forward on every hit — once the edge recovers and the
// hits stop, the key expires and the count (and the scheduler's penalty) clears
// on its own.
//
// KEYS[1] = count key, ARGV[1] = windowSec. Returns: count.
var anthropicSaturationIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local window = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, window)
	end

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

func (c *anthropicSaturationCounterCache) GetSaturationBatch(ctx context.Context, accountIDs []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	keys := make([]string, len(accountIDs))
	for i, id := range accountIDs {
		keys[i] = anthropicSaturationKey(id)
	}
	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget anthropic saturation: %w", err)
	}
	for i, v := range vals {
		if v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		n, convErr := strconv.ParseInt(s, 10, 64)
		if convErr != nil {
			continue
		}
		if n != 0 {
			out[accountIDs[i]] = n
		}
	}
	return out, nil
}
