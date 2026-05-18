package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	anthropicSigPreemptCountPrefix = "anthropic_sig_error_count:account:"
	anthropicSigPreemptFlagPrefix  = "anthropic_sig_preempt:account:"
)

// anthropicSigPreemptArmScript atomically:
//  1. INCR the per-account counter; if it just became 1, EXPIRE it to windowSec.
//  2. If count >= threshold and the preempt flag is not yet set, SETEX the flag
//     to "1" with cooldownSec TTL and return armedNow=1.
//  3. Otherwise armedNow=0.
//
// KEYS[1] = count key, KEYS[2] = flag key
// ARGV[1] = threshold, ARGV[2] = windowSec, ARGV[3] = cooldownSec
// Returns: {count, armedNow}
var anthropicSigPreemptArmScript = redis.NewScript(`
	local count_key = KEYS[1]
	local flag_key = KEYS[2]
	local threshold = tonumber(ARGV[1])
	local window = tonumber(ARGV[2])
	local cooldown = tonumber(ARGV[3])

	local count = redis.call('INCR', count_key)
	if count == 1 then
		redis.call('EXPIRE', count_key, window)
	end

	local armed_now = 0
	if count >= threshold then
		if redis.call('EXISTS', flag_key) == 0 then
			redis.call('SET', flag_key, '1', 'EX', cooldown)
			armed_now = 1
		end
	end

	return {count, armed_now}
`)

type anthropicSignaturePreemptCache struct {
	rdb *redis.Client
}

func NewAnthropicSignaturePreemptCache(rdb *redis.Client) service.AnthropicSignaturePreemptCache {
	return &anthropicSignaturePreemptCache{rdb: rdb}
}

func (c *anthropicSignaturePreemptCache) ArmIfThreshold(ctx context.Context, accountID int64, threshold, windowSeconds, cooldownSeconds int) (int64, bool, error) {
	if threshold <= 0 || windowSeconds <= 0 || cooldownSeconds <= 0 {
		return 0, false, fmt.Errorf("invalid threshold/window/cooldown: %d/%d/%d", threshold, windowSeconds, cooldownSeconds)
	}
	countKey := fmt.Sprintf("%s%d", anthropicSigPreemptCountPrefix, accountID)
	flagKey := fmt.Sprintf("%s%d", anthropicSigPreemptFlagPrefix, accountID)

	raw, err := anthropicSigPreemptArmScript.Run(ctx, c.rdb, []string{countKey, flagKey}, threshold, windowSeconds, cooldownSeconds).Result()
	if err != nil {
		return 0, false, fmt.Errorf("arm signature preempt: %w", err)
	}
	arr, ok := raw.([]any)
	if !ok || len(arr) != 2 {
		return 0, false, fmt.Errorf("arm signature preempt: unexpected reply shape: %T", raw)
	}
	count, _ := arr[0].(int64)
	armed, _ := arr[1].(int64)
	return count, armed == 1, nil
}

func (c *anthropicSignaturePreemptCache) IsArmed(ctx context.Context, accountID int64) (bool, error) {
	flagKey := fmt.Sprintf("%s%d", anthropicSigPreemptFlagPrefix, accountID)
	n, err := c.rdb.Exists(ctx, flagKey).Result()
	if err != nil {
		return false, fmt.Errorf("check signature preempt flag: %w", err)
	}
	return n > 0, nil
}

func (c *anthropicSignaturePreemptCache) Reset(ctx context.Context, accountID int64) error {
	countKey := fmt.Sprintf("%s%d", anthropicSigPreemptCountPrefix, accountID)
	flagKey := fmt.Sprintf("%s%d", anthropicSigPreemptFlagPrefix, accountID)
	return c.rdb.Del(ctx, countKey, flagKey).Err()
}
