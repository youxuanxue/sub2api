package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const antigravitySaturationCountPrefix = "antigravity_saturation_count:account:"

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
