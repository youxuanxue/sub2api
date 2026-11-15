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

func (s *kiroCacheFingerprintStore) GetMany(ctx context.Context, sessionKey string, fingerprintHexes []string) ([]int, error) {
	out := make([]int, len(fingerprintHexes))
	if s == nil || s.rdb == nil || sessionKey == "" || len(fingerprintHexes) == 0 {
		return out, nil
	}
	keys := make([]string, len(fingerprintHexes))
	for i, fingerprintHex := range fingerprintHexes {
		keys[i] = s.key(sessionKey, fingerprintHex)
	}
	values, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		raw, ok := value.(string)
		if !ok {
			continue
		}
		if tokens, parseErr := strconv.Atoi(raw); parseErr == nil && tokens > 0 {
			out[i] = tokens
		}
	}
	return out, nil
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

func (s *kiroCacheFingerprintStore) PutMany(ctx context.Context, sessionKey string, entries []kiroproto.CacheFingerprintEntry, ttl time.Duration) error {
	if s == nil || s.rdb == nil || sessionKey == "" || ttl <= 0 || len(entries) == 0 {
		return nil
	}
	filtered := make([]kiroproto.CacheFingerprintEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.FingerprintHex != "" && entry.Tokens > 0 {
			filtered = append(filtered, entry)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	keys := make([]string, len(filtered))
	for i, entry := range filtered {
		keys[i] = s.key(sessionKey, entry.FingerprintHex)
	}
	remaining := make([]*redis.DurationCmd, len(keys))
	if _, err := s.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for i, key := range keys {
			remaining[i] = pipe.TTL(ctx, key)
		}
		return nil
	}); err != nil {
		return err
	}
	effectiveTTL := make([]time.Duration, len(filtered))
	for i := range filtered {
		effectiveTTL[i] = ttl
		if err := remaining[i].Err(); err == nil && remaining[i].Val() > ttl {
			effectiveTTL[i] = remaining[i].Val()
		}
	}
	_, err := s.rdb.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for i, entry := range filtered {
			pipe.Set(ctx, keys[i], strconv.Itoa(entry.Tokens), effectiveTTL[i])
		}
		return nil
	})
	return err
}
