//go:build unit

package service

import (
	"context"
	"testing"
	"time"
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
