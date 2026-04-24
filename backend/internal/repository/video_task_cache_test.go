//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// TestVideoTaskCache_RoundTrip_InMemory ensures a record saved without
// Redis is recoverable on the same instance. Tokenkey deployments without
// Redis (single-replica dev / CI) MUST still be able to poll a video task
// they just submitted, otherwise the "newapi+volcengine video" feature is
// useless in default dev environments.
func TestVideoTaskCache_RoundTrip_InMemory(t *testing.T) {
	c := NewVideoTaskCache(nil)
	rec := &service.VideoTaskRecord{
		PublicTaskID:   "vt_unit_1",
		UpstreamTaskID: "cgt-volc-xyz",
		AccountID:      11,
		ChannelType:    45,
		BaseURL:        "https://ark.cn-beijing.volces.com",
		APIKey:         "k1",
		OriginModel:    "doubao-seedance",
	}
	if err := c.Save(context.Background(), rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok := c.Lookup(context.Background(), "vt_unit_1")
	if !ok {
		t.Fatal("Lookup miss for known id")
	}
	if got.UpstreamTaskID != rec.UpstreamTaskID || got.ChannelType != 45 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// TestVideoTaskCache_Lookup_Miss verifies the in-memory lookup returns
// false for unknown ids; a stale poll must produce 404, never silently leak
// another user's task.
func TestVideoTaskCache_Lookup_Miss(t *testing.T) {
	c := NewVideoTaskCache(nil)
	if _, ok := c.Lookup(context.Background(), "vt_does_not_exist"); ok {
		t.Fatal("expected lookup miss")
	}
	if _, ok := c.Lookup(context.Background(), ""); ok {
		t.Fatal("expected lookup miss for empty id")
	}
}

// TestVideoTaskCache_Delete_RemovesEntry: terminal status from upstream
// should bound storage. A second poll after Delete must 404 — we MUST NOT
// keep returning a cached "succeeded" status indefinitely.
func TestVideoTaskCache_Delete_RemovesEntry(t *testing.T) {
	c := NewVideoTaskCache(nil)
	rec := &service.VideoTaskRecord{PublicTaskID: "vt_delete_me", UpstreamTaskID: "u", ChannelType: 45}
	_ = c.Save(context.Background(), rec)
	c.Delete(context.Background(), "vt_delete_me")
	if _, ok := c.Lookup(context.Background(), "vt_delete_me"); ok {
		t.Fatal("expected entry gone after Delete")
	}
}

// TestVideoTaskCache_SaveSetsCreatedAt: callers may rely on CreatedAt to
// expire UI-side; the cache MUST stamp it when zero so a malformed caller
// doesn't end up with epoch.
func TestVideoTaskCache_SaveSetsCreatedAt(t *testing.T) {
	c := NewVideoTaskCache(nil)
	rec := &service.VideoTaskRecord{PublicTaskID: "vt_ts", UpstreamTaskID: "u", ChannelType: 45}
	before := time.Now().Add(-time.Second)
	_ = c.Save(context.Background(), rec)
	got, _ := c.Lookup(context.Background(), "vt_ts")
	if got.CreatedAt.Before(before) {
		t.Fatalf("CreatedAt not stamped: %v", got.CreatedAt)
	}
}

// TestVideoTaskCache_RedisIsSingleSourceOfTruth verifies the design
// invariant that the constructor returns DIFFERENT implementations
// depending on whether rdb is nil. Production deployments wire Redis and
// stay on the redis-backed impl; mixing Redis + in-memory in the same
// instance would re-introduce cross-replica staleness (replica A's mem
// invisible to B's Delete) and unbounded memory growth.
//
// We construct a real *redis.Client pointing at an unreachable port so no
// network IO actually happens during the test; we only exercise the
// dispatch-by-impl-type behaviour.
func TestVideoTaskCache_RedisIsSingleSourceOfTruth(t *testing.T) {
	devCache := NewVideoTaskCache(nil)
	if _, ok := devCache.(*videoTaskMemCache); !ok {
		t.Fatalf("nil rdb MUST yield in-memory cache, got %T", devCache)
	}

	prodRdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer func() { _ = prodRdb.Close() }()
	prodCache := NewVideoTaskCache(prodRdb)
	if _, ok := prodCache.(*videoTaskRedisCache); !ok {
		t.Fatalf("non-nil rdb MUST yield redis-backed cache, got %T", prodCache)
	}

	// Lookup via redis-backed cache against unreachable Redis MUST return
	// (nil, false) — never silently fall back to a process-local cache
	// (the impl HAS no process-local fallback by design).
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, ok := prodCache.Lookup(ctx, "vt_anything"); ok {
		t.Fatal("Lookup with unreachable Redis MUST return false, not silently use memory")
	}
}
