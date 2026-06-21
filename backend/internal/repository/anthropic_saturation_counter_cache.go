package repository

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	anthropicSaturationCountPrefix = "anthropic_saturation_count:account:"
	anthropicSaturationFirstPrefix = "anthropic_saturation_first:account:"
	anthropicSaturationLastPrefix  = "anthropic_saturation_last:account:"

	// anthropicSaturationStreakTTLSeconds is the SLIDING TTL of the firstSeen /
	// lastSeen streak keys (refreshed on every hit). It bounds the max gap between
	// two consecutive capacity-envelope hits that still counts as ONE continuous
	// streak: gaps longer than this expire the keys and reset the streak (so an
	// edge that recovered then later re-failed starts a fresh streak, not a
	// resumed one). MUST stay > the sustained-saturation min-age gate in the
	// service layer (anthropicSustainedSaturationMinAgeSeconds) so a genuinely
	// sustained outage can accumulate enough span to trip the hard floor before
	// its keys expire. Self-clearing: once hits stop the keys lapse on their own.
	anthropicSaturationStreakTTLSeconds = 150
)

// anthropicSaturationIncrScript atomically maintains TWO things per account:
//
//  1. The fixed-window count (KEYS[1]): INCR, and on first hit EXPIRE to
//     windowSec. A sustained burst keeps the ORIGINAL fixed window instead of
//     sliding it — once the edge recovers and hits stop, the count (and the
//     bounded soft preference it drives) clears on its own.
//  2. The sliding streak pair (KEYS[2]=firstSeen, KEYS[3]=lastSeen): firstSeen is
//     set once per streak (NX) and lastSeen on every hit; BOTH carry a sliding TTL
//     refreshed each hit. Their span (lastSeen-firstSeen) is the sustained-
//     saturation signal that drives the HARD exclusion. The sliding TTL means a
//     gap longer than streakTTL expires the pair and resets the streak.
//
// The streak epochs are stamped with the Redis server clock (redis.call('TIME')),
// so the sustained signal span (lastSeen-firstSeen) is computed from two values on
// the SAME clock — no app/Redis skew, and the predicate needs no wall clock at all.
//
// KEYS[1]=count, KEYS[2]=firstSeen, KEYS[3]=lastSeen.
// ARGV[1]=windowSec, ARGV[2]=streakTTLSec. Returns: count.
var anthropicSaturationIncrScript = redis.NewScript(`
	local countKey = KEYS[1]
	local firstKey = KEYS[2]
	local lastKey  = KEYS[3]
	local window   = tonumber(ARGV[1])
	local streakTTL= tonumber(ARGV[2])
	local now      = redis.call('TIME')[1]

	local count = redis.call('INCR', countKey)
	if count == 1 then
		redis.call('EXPIRE', countKey, window)
	end

	-- firstSeen: set once per streak (NX); SET NX EX already stamps the TTL, so
	-- only slide it on the not-set (key-exists) branch.
	if redis.call('SET', firstKey, now, 'NX', 'EX', streakTTL) == false then
		redis.call('EXPIRE', firstKey, streakTTL)
	end
	-- lastSeen: always overwrite + (re)stamp the sliding TTL.
	redis.call('SET', lastKey, now, 'EX', streakTTL)

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

func anthropicSaturationFirstKey(accountID int64) string {
	return fmt.Sprintf("%s%d", anthropicSaturationFirstPrefix, accountID)
}

func anthropicSaturationLastKey(accountID int64) string {
	return fmt.Sprintf("%s%d", anthropicSaturationLastPrefix, accountID)
}

func (c *anthropicSaturationCounterCache) IncrementSaturation(ctx context.Context, accountID int64, windowSeconds int) (int64, error) {
	if windowSeconds <= 0 {
		return 0, fmt.Errorf("invalid window: %d", windowSeconds)
	}
	keys := []string{
		anthropicSaturationKey(accountID),
		anthropicSaturationFirstKey(accountID),
		anthropicSaturationLastKey(accountID),
	}
	count, err := anthropicSaturationIncrScript.Run(
		ctx, c.rdb, keys,
		windowSeconds, anthropicSaturationStreakTTLSeconds,
	).Int64()
	if err != nil {
		return 0, fmt.Errorf("increment anthropic saturation: %w", err)
	}
	return count, nil
}

// GetSaturationStreakBatch reads the firstSeen/lastSeen streak pair for each id in
// one MGET (2 keys per account). Accounts with no active streak (both keys
// absent/expired) are omitted from the result.
func (c *anthropicSaturationCounterCache) GetSaturationStreakBatch(ctx context.Context, accountIDs []int64) (map[int64]service.AnthropicSaturationStreak, error) {
	out := make(map[int64]service.AnthropicSaturationStreak, len(accountIDs))
	if len(accountIDs) == 0 {
		return out, nil
	}
	keys := make([]string, 0, len(accountIDs)*2)
	for _, id := range accountIDs {
		keys = append(keys, anthropicSaturationFirstKey(id), anthropicSaturationLastKey(id))
	}
	vals, err := c.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget anthropic saturation streak: %w", err)
	}
	for i, id := range accountIDs {
		first := parseRedisInt64(vals[2*i])
		last := parseRedisInt64(vals[2*i+1])
		if first > 0 || last > 0 {
			out[id] = service.AnthropicSaturationStreak{FirstSeenUnix: first, LastSeenUnix: last}
		}
	}
	return out, nil
}

// parseRedisInt64 converts an MGet element (string or nil) to int64; non-string /
// unparseable / absent values yield 0.
func parseRedisInt64(v any) int64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
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
