package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const antigravitySaturationCountPrefix = "antigravity_saturation_count:account:"

var antigravitySaturationIncrScript = saturationRollingIncrScript

type antigravitySaturationCounterCache struct {
	rdb *redis.Client
}

func NewAntigravitySaturationCounterCache(rdb *redis.Client) service.AntigravitySaturationCounterCache {
	return &antigravitySaturationCounterCache{rdb: rdb}
}

func antigravitySaturationKey(accountID int64, modelKey string) string {
	return fmt.Sprintf("%s%d:model:%s", antigravitySaturationCountPrefix, accountID, modelKey) + saturationRollingKeySuffix
}

func (c *antigravitySaturationCounterCache) GetSaturationBatch(ctx context.Context, scopes []service.AntigravitySaturationScope, windowSeconds int) (map[service.AntigravitySaturationScope]int64, error) {
	out := make(map[service.AntigravitySaturationScope]int64, len(scopes))
	if len(scopes) == 0 {
		return out, nil
	}
	cutoff, err := rollingSaturationCutoffMS(ctx, c.rdb, windowSeconds)
	if err != nil {
		return nil, fmt.Errorf("get antigravity saturation time: %w", err)
	}
	keys := make([]string, len(scopes))
	for i, scope := range scopes {
		keys[i] = antigravitySaturationKey(scope.AccountID, scope.ModelKey)
	}
	counts, err := zcountRollingSaturation(ctx, c.rdb, keys, cutoff)
	if err != nil {
		return nil, fmt.Errorf("zcount antigravity saturation: %w", err)
	}
	for i, count := range counts {
		if count != 0 {
			out[scopes[i]] = count
		}
	}
	return out, nil
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
