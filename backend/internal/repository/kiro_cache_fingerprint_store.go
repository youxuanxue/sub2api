package repository

import (
	"context"
	"fmt"
	"strconv"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/redis/go-redis/v9"
)

const kiroCacheFingerprintPrefix = "kiro_cache_fp:"

type kiroCacheFingerprintStore struct {
	rdb *redis.Client
}

// NewKiroCacheFingerprintStore persists Kiro prompt-prefix fingerprints in Redis.
// A nil client falls back to the in-process memory store so unit/dev paths still work.
func NewKiroCacheFingerprintStore(rdb *redis.Client) kiroproto.CacheFingerprintStore {
	if rdb == nil {
		return kiroproto.NewMemoryCacheFingerprintStore()
	}
	return &kiroCacheFingerprintStore{rdb: rdb}
}

func (s *kiroCacheFingerprintStore) key(sessionKey, fingerprintHex string) string {
	return fmt.Sprintf("%s%s:%s", kiroCacheFingerprintPrefix, sessionKey, fingerprintHex)
}

func (s *kiroCacheFingerprintStore) Get(ctx context.Context, sessionKey, fingerprintHex string) (int, bool, error) {
	if s == nil || s.rdb == nil || sessionKey == "" || fingerprintHex == "" {
		return 0, false, nil
	}
	raw, err := s.rdb.Get(ctx, s.key(sessionKey, fingerprintHex)).Result()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	tokens, err := strconv.Atoi(raw)
	if err != nil || tokens <= 0 {
		return 0, false, nil
	}
	return tokens, true, nil
}

func (s *kiroCacheFingerprintStore) Put(ctx context.Context, sessionKey, fingerprintHex string, tokens int, ttl time.Duration) error {
	if s == nil || s.rdb == nil || sessionKey == "" || fingerprintHex == "" || tokens <= 0 || ttl <= 0 {
		return nil
	}
	key := s.key(sessionKey, fingerprintHex)
	// Keep the longer remaining TTL when the key already exists.
	remaining, err := s.rdb.TTL(ctx, key).Result()
	if err == nil && remaining > ttl {
		ttl = remaining
	}
	return s.rdb.Set(ctx, key, strconv.Itoa(tokens), ttl).Err()
}
