package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	videoTaskCacheKeyPrefix = "tk:newapi:video_task:"
	videoTaskCacheTTL       = 24 * time.Hour
)

func videoTaskCacheKey(publicTaskID string) string {
	return videoTaskCacheKeyPrefix + publicTaskID
}

// NewVideoTaskCache returns the Redis-backed implementation of
// service.VideoTaskCache. When rdb is nil (unit tests, broken env), the
// returned implementation falls back to a process-local in-memory map. We
// intentionally do NOT layer Redis + memory in the same instance —
// production deployments configure Redis and stay on the Redis path; tests
// pass nil and stay on the memory path. Mixing the two would re-introduce
// a cross-replica staleness leak (replica A's mem invisible to B's Delete)
// and unbounded memory growth (in-memory entries only removed by
// Delete-on-terminal).
func NewVideoTaskCache(rdb *redis.Client) service.VideoTaskCache {
	if rdb == nil {
		return &videoTaskMemCache{mem: make(map[string]*service.VideoTaskRecord)}
	}
	return &videoTaskRedisCache{rdb: rdb, ttl: videoTaskCacheTTL}
}

// videoTaskRedisCache is the production implementation. Redis is the single
// source of truth; on Set error the upstream task has already been billed,
// so we log + swallow rather than orphan it.
type videoTaskRedisCache struct {
	rdb *redis.Client
	ttl time.Duration
}

func (c *videoTaskRedisCache) Save(ctx context.Context, record *service.VideoTaskRecord) error {
	if record == nil || strings.TrimSpace(record.PublicTaskID) == "" {
		return errors.New("video task record requires public_task_id")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal video task record: %w", err)
	}
	if redisErr := c.rdb.Set(ctx, videoTaskCacheKey(record.PublicTaskID), payload, c.ttl).Err(); redisErr != nil {
		logger.L().Warn("video_task_cache.redis_set_failed",
			zap.String("public_task_id", record.PublicTaskID),
			zap.Error(redisErr),
		)
	}
	return nil
}

func (c *videoTaskRedisCache) Lookup(ctx context.Context, publicTaskID string) (*service.VideoTaskRecord, bool) {
	publicTaskID = strings.TrimSpace(publicTaskID)
	if publicTaskID == "" {
		return nil, false
	}
	raw, err := c.rdb.Get(ctx, videoTaskCacheKey(publicTaskID)).Bytes()
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var rec service.VideoTaskRecord
	if err := json.Unmarshal(raw, &rec); err != nil || rec.PublicTaskID != publicTaskID {
		return nil, false
	}
	return &rec, true
}

func (c *videoTaskRedisCache) Delete(ctx context.Context, publicTaskID string) {
	publicTaskID = strings.TrimSpace(publicTaskID)
	if publicTaskID == "" {
		return
	}
	_ = c.rdb.Del(ctx, videoTaskCacheKey(publicTaskID)).Err()
}

// videoTaskMemCache is the test / no-Redis fallback. Single-replica only; for
// production use NewVideoTaskCache with a non-nil *redis.Client. No TTL — the
// tests that exercise this path either Delete explicitly or accept that the
// process is short-lived.
type videoTaskMemCache struct {
	mu  sync.RWMutex
	mem map[string]*service.VideoTaskRecord
}

func (c *videoTaskMemCache) Save(_ context.Context, record *service.VideoTaskRecord) error {
	if record == nil || strings.TrimSpace(record.PublicTaskID) == "" {
		return errors.New("video task record requires public_task_id")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	c.mu.Lock()
	c.mem[record.PublicTaskID] = record
	c.mu.Unlock()
	return nil
}

func (c *videoTaskMemCache) Lookup(_ context.Context, publicTaskID string) (*service.VideoTaskRecord, bool) {
	publicTaskID = strings.TrimSpace(publicTaskID)
	if publicTaskID == "" {
		return nil, false
	}
	c.mu.RLock()
	rec, ok := c.mem[publicTaskID]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	cp := *rec
	return &cp, true
}

func (c *videoTaskMemCache) Delete(_ context.Context, publicTaskID string) {
	publicTaskID = strings.TrimSpace(publicTaskID)
	if publicTaskID == "" {
		return
	}
	c.mu.Lock()
	delete(c.mem, publicTaskID)
	c.mu.Unlock()
}
