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

// TestTkAccountUsesNewAPIBridgeForVideo_Truth covers the gating predicate so
// a regression that, e.g., accidentally returns true for ChannelType=0 (no
// adaptor configured) is caught at compile-of-test time.
func TestTkAccountUsesNewAPIBridgeForVideo_Truth(t *testing.T) {
	cases := []struct {
		name     string
		account  *Account
		endpoint string
		want     bool
	}{
		{"nil_account", nil, BridgeEndpointVideoSubmit, false},
		{"zero_channel_type", &Account{ChannelType: 0}, BridgeEndpointVideoSubmit, false},
		{"non_video_endpoint", &Account{ChannelType: 45}, BridgeEndpointChatCompletions, false},
		{"video_submit_ok", &Account{ChannelType: 45}, BridgeEndpointVideoSubmit, true},
		{"video_fetch_ok", &Account{ChannelType: 45}, BridgeEndpointVideoFetch, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TkAccountUsesNewAPIBridgeForVideo(nil, tc.account, tc.endpoint)
			if got != tc.want {
				t.Fatalf("TkAccountUsesNewAPIBridgeForVideo(%s, channel=%d)=%v want %v",
					tc.endpoint, accountChannelType(tc.account), got, tc.want)
			}
		})
	}
}

func accountChannelType(a *Account) int {
	if a == nil {
		return -1
	}
	return a.ChannelType
}
