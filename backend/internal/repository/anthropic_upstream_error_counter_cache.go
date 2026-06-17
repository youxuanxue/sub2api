package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	anthropicUpstreamErrorCounterPrefix   = "anthropic_upstream_error_count:account:"
	anthropicCooldownTierPrefix           = "anthropic_cooldown_tier:account:"
	anthropicCooldownEscalationSlotPrefix = "anthropic_cooldown_escalation_slot:account:"
	// anthropicBodyless403CounterPrefix is a DISTINCT namespace from the general
	// upstream-error counter so the bodyless-403 terminal-disable threshold is
	// driven ONLY by empty/unstructured 403s, never polluted by 429/5xx.
	anthropicBodyless403CounterPrefix = "anthropic_bodyless_403_count:account:"
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

func (c *anthropicUpstreamErrorCounterCache) IncrementAnthropicBodyless403Count(ctx context.Context, accountID int64, windowMinutes int) (int64, error) {
	key := fmt.Sprintf("%s%d", anthropicBodyless403CounterPrefix, accountID)
	ttlSeconds := windowMinutes * 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}

	result, err := anthropicUpstreamErrorCounterIncrScript.Run(ctx, c.rdb, []string{key}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment anthropic bodyless 403 count: %w", err)
	}
	return result, nil
}

func (c *anthropicUpstreamErrorCounterCache) ResetAnthropicBodyless403Count(ctx context.Context, accountID int64) error {
	key := fmt.Sprintf("%s%d", anthropicBodyless403CounterPrefix, accountID)
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

func (c *anthropicUpstreamErrorCounterCache) IncrementAnthropicCooldownTierEscalations(ctx context.Context, ttlMinutes int) (int64, error) {
	ttlSeconds := ttlMinutes * 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}

	result, err := anthropicUpstreamErrorCounterIncrScript.Run(ctx, c.rdb, []string{service.AnthropicCooldownTierEscalationsKey}, ttlSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment anthropic cooldown tier escalations: %w", err)
	}
	return result, nil
}

func (c *anthropicUpstreamErrorCounterCache) GetAnthropicCooldownTierEscalations(ctx context.Context) (int64, error) {
	val, err := c.rdb.Get(ctx, service.AnthropicCooldownTierEscalationsKey).Int64()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, fmt.Errorf("get anthropic cooldown tier escalations: %w", err)
	}
	return val, nil
}

func (c *anthropicUpstreamErrorCounterCache) AcquireAnthropicCooldownEscalationSlot(ctx context.Context, accountID int64, ttlSeconds int) (bool, error) {
	key := fmt.Sprintf("%s%d", anthropicCooldownEscalationSlotPrefix, accountID)
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	ok, err := c.rdb.SetNX(ctx, key, 1, time.Duration(ttlSeconds)*time.Second).Result()
	if err != nil {
		return false, fmt.Errorf("acquire anthropic cooldown escalation slot: %w", err)
	}
	return ok, nil
}

func (c *anthropicUpstreamErrorCounterCache) SetAnthropicCooldownEscalationSlotTTL(ctx context.Context, accountID int64, ttlSeconds int) error {
	key := fmt.Sprintf("%s%d", anthropicCooldownEscalationSlotPrefix, accountID)
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	return c.rdb.Expire(ctx, key, time.Duration(ttlSeconds)*time.Second).Err()
}

func (c *anthropicUpstreamErrorCounterCache) ResetAnthropicCooldownEscalationSlot(ctx context.Context, accountID int64) error {
	key := fmt.Sprintf("%s%d", anthropicCooldownEscalationSlotPrefix, accountID)
	return c.rdb.Del(ctx, key).Err()
}
