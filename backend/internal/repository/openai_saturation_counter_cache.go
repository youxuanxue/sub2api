package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const openaiSaturationCountPrefix = "openai_saturation_count:account:"

var candidateFailureIncrScript = redis.NewScript(`
	local key = KEYS[1]
	local window = tonumber(ARGV[1])

	local count = redis.call('INCR', key)
	if count == 1 then
		redis.call('EXPIRE', key, window)
	end

	return count
`)

var openaiSaturationIncrScript = redis.NewScript(`
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
	return candidateFailureIncrScript.Run(ctx, c.rdb, []string{candidateFailureKey(scope)}, windowSeconds).Int64()
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

func (c *openaiSaturationCounterCache) GetSaturationBatch(ctx context.Context, accountIDs []int64, windowSeconds int) (map[int64]int64, error) {
	out := make(map[int64]int64, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	if windowSeconds <= 0 {
		return nil, fmt.Errorf("invalid window: %d", windowSeconds)
	}
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("get openai saturation time: %w", err)
	}
	cutoff := now.Add(-time.Duration(windowSeconds) * time.Second).UnixMilli()
	keys := make([]string, len(accountIDs))
	for i, id := range accountIDs {
		keys[i] = openaiSaturationKey(id)
	}
	pipe := c.rdb.Pipeline()
	cmds := make([]*redis.IntCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.ZCount(ctx, key, "("+strconv.FormatInt(cutoff, 10), "+inf")
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("zcount openai saturation: %w", err)
	}
	for i, cmd := range cmds {
		n, err := cmd.Result()
		if err != nil {
			continue
		}
		if n != 0 {
			out[accountIDs[i]] = n
		}
	}
	return out, nil
}
