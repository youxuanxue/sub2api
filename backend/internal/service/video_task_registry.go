package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// VideoTaskRegistry stores enough metadata about a submitted video-generation
// task so that a later GET /v1/video/generations/:task_id (or the OpenAI-compat
// alias /v1/videos/:task_id) can be routed back to the same upstream account
// and channel without consulting any DB. The fifth platform `newapi` is
// inherently asynchronous for video, so the registry must outlive any single
// HTTP request.
//
// Storage: primarily Redis (shared across replicas, TTL-bounded). When Redis
// is not available (unit tests, broken env), an in-memory map is used so the
// gateway still functions on a single instance. The in-memory fallback is NOT
// safe across replicas; production deployments must have Redis configured.
type VideoTaskRegistry struct {
	rdb *redis.Client
	ttl time.Duration

	mu  sync.RWMutex
	mem map[string]*VideoTaskRecord
}

// VideoTaskRecord is the minimum bridge needs to call FetchTask later.
// AccountID + ChannelType pin the routing target; APIKey is captured at
// submit time because account credentials may rotate before the user polls.
type VideoTaskRecord struct {
	PublicTaskID   string    `json:"public_task_id"`
	UpstreamTaskID string    `json:"upstream_task_id"`
	AccountID      int64     `json:"account_id"`
	UserID         int64     `json:"user_id"`
	GroupID        int64     `json:"group_id"`
	APIKeyID       int64     `json:"api_key_id"`
	ChannelType    int       `json:"channel_type"`
	Platform       string    `json:"platform"`
	BaseURL        string    `json:"base_url"`
	APIKey         string    `json:"api_key"`
	OriginModel    string    `json:"origin_model"`
	UpstreamModel  string    `json:"upstream_model"`
	Action         string    `json:"action"`
	CreatedAt      time.Time `json:"created_at"`
}

const (
	videoTaskRegistryDefaultTTL = 24 * time.Hour
	videoTaskRegistryRedisKey   = "tk:newapi:video_task:"
)

// NewVideoTaskRegistry constructs a registry. rdb may be nil — in that case
// only the in-memory fallback is used.
func NewVideoTaskRegistry(rdb *redis.Client) *VideoTaskRegistry {
	return &VideoTaskRegistry{
		rdb: rdb,
		ttl: videoTaskRegistryDefaultTTL,
		mem: make(map[string]*VideoTaskRecord),
	}
}

// Save persists the record. The in-memory copy is always populated; Redis is
// best-effort and returns a non-nil error only on marshal failure (a Redis
// outage logs a warning but does NOT fail the submit, because the upstream
// task has already been created and quota already recorded — failing here
// would leave the user with no task_id but a billed task running upstream).
//
// Multi-replica deployments must monitor the redisErr path; persistent
// failures degrade polling reliability since a poll arriving on a different
// replica will 404.
func (r *VideoTaskRegistry) Save(ctx context.Context, record *VideoTaskRecord) error {
	if record == nil || strings.TrimSpace(record.PublicTaskID) == "" {
		return errors.New("video task record requires public_task_id")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	r.mu.Lock()
	r.mem[record.PublicTaskID] = record
	r.mu.Unlock()
	if r.rdb == nil {
		return nil
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal video task record: %w", err)
	}
	if redisErr := r.rdb.Set(ctx, r.redisKey(record.PublicTaskID), payload, r.ttl).Err(); redisErr != nil {
		logger.L().Warn("video_task_registry.redis_set_failed",
			zap.String("public_task_id", record.PublicTaskID),
			zap.Error(redisErr),
		)
	}
	return nil
}

// Lookup returns the record for the given public task id. Redis is consulted
// first (so a poll arriving on a different replica still works); on miss the
// in-memory store is checked. A cache-hit from Redis backfills memory for
// faster subsequent polls.
func (r *VideoTaskRegistry) Lookup(ctx context.Context, publicTaskID string) (*VideoTaskRecord, bool) {
	publicTaskID = strings.TrimSpace(publicTaskID)
	if publicTaskID == "" {
		return nil, false
	}
	if r.rdb != nil {
		raw, err := r.rdb.Get(ctx, r.redisKey(publicTaskID)).Bytes()
		if err == nil && len(raw) > 0 {
			var rec VideoTaskRecord
			if jsonErr := json.Unmarshal(raw, &rec); jsonErr == nil && rec.PublicTaskID == publicTaskID {
				r.mu.Lock()
				r.mem[publicTaskID] = &rec
				r.mu.Unlock()
				return &rec, true
			}
		}
	}
	r.mu.RLock()
	rec, ok := r.mem[publicTaskID]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	cp := *rec
	return &cp, true
}

// Delete removes a record. Used when the upstream reports a terminal status
// and the client has acknowledged it.
func (r *VideoTaskRegistry) Delete(ctx context.Context, publicTaskID string) {
	publicTaskID = strings.TrimSpace(publicTaskID)
	if publicTaskID == "" {
		return
	}
	r.mu.Lock()
	delete(r.mem, publicTaskID)
	r.mu.Unlock()
	if r.rdb != nil {
		_ = r.rdb.Del(ctx, r.redisKey(publicTaskID)).Err()
	}
}

func (r *VideoTaskRegistry) redisKey(publicTaskID string) string {
	return videoTaskRegistryRedisKey + publicTaskID
}
