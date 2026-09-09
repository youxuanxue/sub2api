package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const openaiSaturationCountPrefix = "openai_saturation_count:account:"

var openaiSaturationIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local window = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, window)
	end

	return count
`)

type openaiSaturationCounterCache struct {
	rdb *redis.Client
}

func candidateFailureKey(scope service.CandidateFailureScope) string {
	return fmt.Sprintf("candidate_failure:account:%d:model:%x", scope.AccountID, sha256.Sum256([]byte(scope.Model)))
}

func (c *openaiSaturationCounterCache) IncrementCandidateFailure(ctx context.Context, scope service.CandidateFailureScope, windowSeconds int) (int64, error) {
	if scope.AccountID <= 0 || scope.Model == "" || windowSeconds <= 0 {
		return 0, fmt.Errorf("invalid candidate failure scope or window")
	}
	return openaiSaturationIncrScript.Run(ctx, c.rdb, []string{candidateFailureKey(scope)}, windowSeconds).Int64()
}

func (c *openaiSaturationCounterCache) GetCandidateFailures(ctx context.Context, scopes []service.CandidateFailureScope) (map[service.CandidateFailureScope]int64, error) {
	out := make(map[service.CandidateFailureScope]int64, len(scopes))
	if len(scopes) == 0 {
		return out, nil
	}
	keys := make([]string, len(scopes))
	for i, scope := range scopes {
		keys[i] = candidateFailureKey(scope)
	}
	values, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if str, ok := value.(string); ok {
			if n, err := strconv.ParseInt(str, 10, 64); err == nil && n > 0 {
				out[scopes[i]] = n
			}
		}
	}
	return out, nil
}

func NewOpenAISaturationCounterCache(rdb *redis.Client) service.OpenAISaturationCounterCache {
	return &openaiSaturationCounterCache{rdb: rdb}
}

func openaiSaturationKey(accountID int64) string {
	return fmt.Sprintf("%s%d", openaiSaturationCountPrefix, accountID)
}

func (c *openaiSaturationCounterCache) IncrementSaturation(ctx context.Context, accountID int64, windowSeconds int) (int64, error) {
	if windowSeconds <= 0 {
		return 0, fmt.Errorf("invalid window: %d", windowSeconds)
	}
	key := openaiSaturationKey(accountID)
	count, err := openaiSaturationIncrScript.Run(ctx, c.rdb, []string{key}, windowSeconds).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment openai saturation: %w", err)
	}
	return count, nil
}

func (c *openaiSaturationCounterCache) GetSaturationBatch(ctx context.Context, accountIDs []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	keys := make([]string, len(accountIDs))
	for i, id := range accountIDs {
		keys[i] = openaiSaturationKey(id)
	}
	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget openai saturation: %w", err)
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
