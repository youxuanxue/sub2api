//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestVideoTaskRegistry_RoundTrip_InMemory ensures a record saved without
// Redis is recoverable on the same instance. Tokenkey deployments without
// Redis (single-replica dev / CI) MUST still be able to poll a video task
// they just submitted, otherwise the "newapi+volcengine video" feature is
// useless in default dev environments.
func TestVideoTaskRegistry_RoundTrip_InMemory(t *testing.T) {
	r := NewVideoTaskRegistry(nil)
	rec := &VideoTaskRecord{
		PublicTaskID:   "vt_unit_1",
		UpstreamTaskID: "cgt-volc-xyz",
		AccountID:      11,
		ChannelType:    45,
		BaseURL:        "https://ark.cn-beijing.volces.com",
		APIKey:         "k1",
		OriginModel:    "doubao-seedance",
	}
	if err := r.Save(context.Background(), rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok := r.Lookup(context.Background(), "vt_unit_1")
	if !ok {
		t.Fatal("Lookup miss for known id")
	}
	if got.UpstreamTaskID != rec.UpstreamTaskID || got.ChannelType != 45 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// TestVideoTaskRegistry_Lookup_Miss verifies the in-memory lookup returns
// false for unknown ids; a stale poll must produce 404, never silently leak
// another user's task.
func TestVideoTaskRegistry_Lookup_Miss(t *testing.T) {
	r := NewVideoTaskRegistry(nil)
	if _, ok := r.Lookup(context.Background(), "vt_does_not_exist"); ok {
		t.Fatal("expected lookup miss")
	}
	if _, ok := r.Lookup(context.Background(), ""); ok {
		t.Fatal("expected lookup miss for empty id")
	}
}

// TestVideoTaskRegistry_Delete_RemovesEntry: terminal status from upstream
// should bound storage. A second poll after Delete must 404 — we MUST NOT
// keep returning a cached "succeeded" status indefinitely.
func TestVideoTaskRegistry_Delete_RemovesEntry(t *testing.T) {
	r := NewVideoTaskRegistry(nil)
	rec := &VideoTaskRecord{PublicTaskID: "vt_delete_me", UpstreamTaskID: "u", ChannelType: 45}
	_ = r.Save(context.Background(), rec)
	r.Delete(context.Background(), "vt_delete_me")
	if _, ok := r.Lookup(context.Background(), "vt_delete_me"); ok {
		t.Fatal("expected entry gone after Delete")
	}
}

// TestVideoTaskRegistry_SaveSetsCreatedAt: callers may rely on CreatedAt to
// expire UI-side; the registry MUST stamp it when zero so a malformed caller
// doesn't end up with epoch.
func TestVideoTaskRegistry_SaveSetsCreatedAt(t *testing.T) {
	r := NewVideoTaskRegistry(nil)
	rec := &VideoTaskRecord{PublicTaskID: "vt_ts", UpstreamTaskID: "u", ChannelType: 45}
	before := time.Now().Add(-time.Second)
	_ = r.Save(context.Background(), rec)
	got, _ := r.Lookup(context.Background(), "vt_ts")
	if got.CreatedAt.Before(before) {
		t.Fatalf("CreatedAt not stamped: %v", got.CreatedAt)
	}
}

// TestVideoTaskRegistry_RedisIsSingleSourceOfTruth verifies the design
// invariant that the in-memory map is allocated ONLY when rdb is nil. When a
// real Redis client is wired (every production deployment), the registry
// MUST NOT keep a parallel in-memory copy — doing so re-introduces the
// cross-replica leak (replica A serves a stale "succeeded" status because
// replica B's Delete couldn't reach A's memory) and the unbounded-memory
// growth (no eviction beyond Delete-on-terminal).
//
// We construct a real *redis.Client pointing at an unreachable port so no
// network IO actually happens during the test, but the rdb!=nil branch is
// taken. Save+Lookup go through Redis; with no Redis behind, Save logs +
// nil-returns and Lookup miss-returns false. The point is mem stays nil.
func TestVideoTaskRegistry_RedisIsSingleSourceOfTruth(t *testing.T) {
	// Single-replica dev path (rdb=nil) must allocate mem.
	devReg := NewVideoTaskRegistry(nil)
	if devReg.mem == nil {
		t.Fatal("rdb=nil registry MUST allocate in-memory fallback (single-replica dev path)")
	}

	// Production path (rdb!=nil): mem MUST be nil so Lookup cannot fall
	// through to a stale local copy on Redis miss. We use a real client
	// configured for an unreachable address; no command actually dials
	// in this test (we only inspect the field).
	prodRdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer func() { _ = prodRdb.Close() }()
	prodReg := NewVideoTaskRegistry(prodRdb)
	if prodReg.mem != nil {
		t.Fatal("rdb!=nil registry MUST leave mem nil — Redis is the single source of truth")
	}

	// Lookup with rdb non-nil and Redis unreachable MUST return (nil,false)
	// — we MUST NOT silently fall back to memory (which would defeat the
	// entire single-source-of-truth design when memory has stale data).
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, ok := prodReg.Lookup(ctx, "vt_anything"); ok {
		t.Fatal("Lookup with unreachable Redis MUST return false, not silently use memory")
	}
}

// TestTkBridgeEndpointEnabled_Truth: tkBridgeEndpointEnabled is a pure
// endpoint-name allow-list; the nil-account / channel_type / kill-switch
// preconditions live in the upstream-shape accountUsesNewAPIAdaptorBridge
// caller. Keeping this helper free of those checks is what makes the
// upstream-shape file's diff a single line; a regression that re-adds them
// here would re-bloat the dispatch gate.
func TestTkBridgeEndpointEnabled_Truth(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"video_submit_enabled", BridgeEndpointVideoSubmit, true},
		{"video_fetch_enabled", BridgeEndpointVideoFetch, true},
		{"empty_disabled", "", false},
		{"unknown_disabled", "totally_made_up", false},
		// The four upstream-shape endpoints MUST NOT be answered here —
		// they are handled by the explicit case in
		// accountUsesNewAPIAdaptorBridge. If tkBridgeEndpointEnabled
		// starts answering true for them we have duplicated source of
		// truth and a maintenance hazard.
		{"chat_completions_handled_upstream", BridgeEndpointChatCompletions, false},
		{"responses_handled_upstream", BridgeEndpointResponses, false},
		{"embeddings_handled_upstream", BridgeEndpointEmbeddings, false},
		{"images_handled_upstream", BridgeEndpointImages, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tkBridgeEndpointEnabled(tc.endpoint); got != tc.want {
				t.Fatalf("tkBridgeEndpointEnabled(%q)=%v want %v", tc.endpoint, got, tc.want)
			}
		})
	}
}
