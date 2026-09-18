package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const anthropicSaturationCountPrefix = "anthropic_saturation_count:account:"

// anthropicSaturationIncrScript keeps the historical sentinel symbol; the
// rolling ZSET implementation is shared as saturationRollingIncrScript.
var anthropicSaturationIncrScript = saturationRollingIncrScript

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
	cutoff, err := rollingSaturationCutoffMS(ctx, c.rdb, windowSeconds)
	if err != nil {
		return nil, fmt.Errorf("get anthropic saturation time: %w", err)
	}
	keys := make([]string, len(accountIDs))
	for i, id := range accountIDs {
		keys[i] = anthropicSaturationKey(id)
	}
	counts, err := zcountRollingSaturation(ctx, c.rdb, keys, cutoff)
	if err != nil {
		return nil, fmt.Errorf("zcount anthropic saturation: %w", err)
	}
	for i, n := range counts {
		if n != 0 {
			out[accountIDs[i]] = n
		}
	}
	return out, nil
}
