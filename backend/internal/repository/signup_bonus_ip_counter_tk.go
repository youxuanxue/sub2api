package repository

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/redis/go-redis/v9"
)

const signupBonusIPRedisPrefix = "signup_bonus_ip:"

type signupBonusIPCounter struct {
	rdb *redis.Client
}

// NewSignupBonusIPCounter builds the Redis-backed per-IP signup-bonus counter.
// Returns nil when rdb is nil so Wire can still construct AuthService in tests.
func NewSignupBonusIPCounter(rdb *redis.Client) service.SignupBonusIPCounter {
	if rdb == nil {
		return nil
	}
	return &signupBonusIPCounter{rdb: rdb}
}

func (c *signupBonusIPCounter) IncrDaily(ctx context.Context, ip string) (int64, error) {
	if c == nil || c.rdb == nil {
		return 0, redis.Nil
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return 0, nil
	}
	key := signupBonusIPRedisPrefix + ip + ":" + time.Now().UTC().Format("20060102")
	count, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if count == 1 {
		ttl := time.Until(nextUTCMidnight().Add(2 * time.Minute))
		if ttl < time.Hour {
			ttl = 24 * time.Hour
		}
		_ = c.rdb.Expire(ctx, key, ttl).Err()
	}
	return count, nil
}

func nextUTCMidnight() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
}
