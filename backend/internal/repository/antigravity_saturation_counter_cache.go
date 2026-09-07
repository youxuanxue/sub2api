package repository

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const antigravitySaturationCountPrefix = "antigravity_saturation_count:account:"

func (c *antigravitySaturationCounterCache) GetSaturationBatch(ctx context.Context, scopes []service.AntigravitySaturationScope) (map[service.AntigravitySaturationScope]int64, error) {
	out := make(map[service.AntigravitySaturationScope]int64, len(scopes))
	if len(scopes) == 0 {
		return out, nil
	}
	keys := make([]string, len(scopes))
	for i, scope := range scopes {
		keys[i] = antigravitySaturationKey(scope.AccountID, scope.ModelKey)
	}
	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget antigravity saturation: %w", err)
	}
	for i, value := range values {
		if value == nil {
			continue
		}
		count, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse antigravity saturation: %w", err)
		}
		out[scopes[i]] = count
	}
	return out, nil
}

var antigravitySaturationIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local window = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, window)
	end

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
