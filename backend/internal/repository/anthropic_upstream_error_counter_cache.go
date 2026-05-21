package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	anthropicUpstreamErrorCounterPrefix = "anthropic_upstream_error_count:account:"
	anthropicCooldownTierPrefix         = "anthropic_cooldown_tier:account:"
)

var anthropicUpstreamErrorCounterIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local ttl = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, ttl)
	end

	return count
`)

type anthropicUpstreamErrorCounterCache struct {
	rdb *redis.Client
}

func NewAnthropicUpstreamErrorCounterCache(rdb *redis.Client) service.AnthropicUpstreamErrorCounterCache {
	return &anthropicUpstreamErrorCounterCache{rdb: rdb}
}

func (c *anthropicUpstreamErrorCounterCache) IncrementAnthropicUpstreamErrorCount(ctx context.Context, accountID int64, windowMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", anthropicUpstreamErrorCounterPrefix, accountID)
	ttlSeconds := windowMinutes * 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}

	result, err := anthropicUpstreamErrorCounterIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment anthropic upstream error count: %w", err)
	}
	return result, nil
}

func (c *anthropicUpstreamErrorCounterCache) ResetAnthropicUpstreamErrorCount(ctx context.Context, accountID int64) error {
	key := fmt.Sprintf("%s%d", anthropicUpstreamErrorCounterPrefix, accountID)
	return c.rdb.Del(ctx, key).Err()
}

func (c *anthropicUpstreamErrorCounterCache) IncrementAnthropicCooldownTier(ctx context.Context, accountID int64, ttlMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", anthropicCooldownTierPrefix, accountID)
	ttlSeconds := ttlMinutes * 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}

	result, err := anthropicUpstreamErrorCounterIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment anthropic cooldown tier: %w", err)
	}
	return result, nil
}

func (c *anthropicUpstreamErrorCounterCache) ResetAnthropicCooldownTier(ctx context.Context, accountID int64) error {
	key := fmt.Sprintf("%s%d", anthropicCooldownTierPrefix, accountID)
	return c.rdb.Del(ctx, key).Err()
}
