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
// Storage model: **Redis is the single source of truth when configured**;
// the in-memory map is a strict fallback used only when rdb is nil (unit
// tests, broken env). We intentionally do NOT use the in-memory map as a
// secondary cache when Redis is present, because doing so would:
//
//   - Leak memory in any deployment where users submit but never poll
//     (no eviction beyond Delete-on-terminal-status).
//   - Break multi-replica correctness: a Delete on replica B could not
//     reach replica A's in-memory copy, so A would still serve a stale
//     "succeeded" status indefinitely.
//
// All production deployments configure Redis (see deploy/docker-compose*.yml);
// the rdb=nil branch exists only for `go test -tags=unit` paths.
type VideoTaskRegistry struct {
	rdb *redis.Client
	ttl time.Duration

	// mem is consulted ONLY when rdb is nil. Touching it when rdb is set
	// would resurrect the cross-replica leak that motivated this design.
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
	CreatedAt      time.Time `json:"created_at"`
}

const (
	videoTaskRegistryDefaultTTL = 24 * time.Hour
	videoTaskRegistryRedisKey   = "tk:newapi:video_task:"
)

// NewVideoTaskRegistry constructs a registry. rdb may be nil — in that case
// only the in-memory fallback is used (single-replica unit tests / broken
// env). Production deployments always pass a non-nil rdb.
func NewVideoTaskRegistry(rdb *redis.Client) *VideoTaskRegistry {
	r := &VideoTaskRegistry{
		rdb: rdb,
		ttl: videoTaskRegistryDefaultTTL,
	}
	if rdb == nil {
		r.mem = make(map[string]*VideoTaskRecord)
	}
	return r
}

// Save persists the record. Redis is the source of truth when configured.
//
// Failure semantics: a Redis Set error is logged and swallowed (returns nil),
// because by the time we reach Save the upstream task has already been
// created and quota recorded — failing the submit here would orphan a billed
// task. The downside is that subsequent polls land on Redis miss and return
// 404; multi-replica operators MUST monitor the
// `video_task_registry.redis_set_failed` warn log to detect this state.
//
// We deliberately do NOT keep a parallel in-memory copy as a safety net,
// because that re-introduces two old failure modes: cross-replica leak
// (replica A's mem invisible to B) and unbounded memory growth (entries
// only removed by Delete-on-terminal). See the type doc for the full
// rationale.
//
// The only non-nil error returned is for invalid input (nil record, empty
// PublicTaskID) or marshal failure — both indicate caller bugs.
func (r *VideoTaskRegistry) Save(ctx context.Context, record *VideoTaskRecord) error {
	if record == nil || strings.TrimSpace(record.PublicTaskID) == "" {
		return errors.New("video task record requires public_task_id")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	if r.rdb == nil {
		r.mu.Lock()
		r.mem[record.PublicTaskID] = record
		r.mu.Unlock()
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

// Lookup returns the record for the given public task id. Redis is the single
// source of truth when configured; on Redis miss / outage we return false (no
// memory fallback) so the handler 404s and the client knows to retry rather
// than acting on a stale local copy.
func (r *VideoTaskRegistry) Lookup(ctx context.Context, publicTaskID string) (*VideoTaskRecord, bool) {
	publicTaskID = strings.TrimSpace(publicTaskID)
	if publicTaskID == "" {
		return nil, false
	}
	if r.rdb == nil {
		r.mu.RLock()
		rec, ok := r.mem[publicTaskID]
		r.mu.RUnlock()
		if !ok {
			return nil, false
		}
		cp := *rec
		return &cp, true
	}
	raw, err := r.rdb.Get(ctx, r.redisKey(publicTaskID)).Bytes()
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var rec VideoTaskRecord
	if err := json.Unmarshal(raw, &rec); err != nil || rec.PublicTaskID != publicTaskID {
		return nil, false
	}
	return &rec, true
}

// Delete removes a record. Used when the upstream reports a terminal status.
func (r *VideoTaskRegistry) Delete(ctx context.Context, publicTaskID string) {
	publicTaskID = strings.TrimSpace(publicTaskID)
	if publicTaskID == "" {
		return
	}
	if r.rdb == nil {
		r.mu.Lock()
		delete(r.mem, publicTaskID)
		r.mu.Unlock()
		return
	}
	_ = r.rdb.Del(ctx, r.redisKey(publicTaskID)).Err()
}

func (r *VideoTaskRegistry) redisKey(publicTaskID string) string {
	return videoTaskRegistryRedisKey + publicTaskID
}
