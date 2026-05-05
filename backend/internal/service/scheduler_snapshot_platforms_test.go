//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestAllSchedulingPlatforms_IncludesNewAPI is the structural guard for
// the canonical platform list. Any scheduler caller that needs to enumerate
// platforms must derive from AllSchedulingPlatforms() — adding a sixth
// platform must not require touching individual callers.
func TestAllSchedulingPlatforms_IncludesNewAPI(t *testing.T) {
	got := AllSchedulingPlatforms()
	want := map[string]bool{
		PlatformAnthropic:   false,
		PlatformGemini:      false,
		PlatformOpenAI:      false,
		PlatformAntigravity: false,
		PlatformNewAPI:      false,
	}
	for _, p := range got {
		if _, ok := want[p]; !ok {
			t.Fatalf("AllSchedulingPlatforms returned unexpected platform %q (got=%v)", p, got)
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Fatalf("AllSchedulingPlatforms missing platform %q (got=%v) — would re-introduce the original P0 regression where group_change events skip rebuilding this platform's bucket", p, got)
		}
	}
}

// recordingSchedulerCache implements SchedulerCache and records every
// (bucket, op) pair so tests can assert which buckets were attempted.
type recordingSchedulerCache struct {
	mu       sync.Mutex
	locks    []SchedulerBucket
	sets     []SchedulerBucket
	accounts []Account
}

func (c *recordingSchedulerCache) GetSnapshot(ctx context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, nil
}

func (c *recordingSchedulerCache) SetSnapshot(ctx context.Context, bucket SchedulerBucket, accounts []Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sets = append(c.sets, bucket)
	return nil
}

func (c *recordingSchedulerCache) GetAccount(ctx context.Context, accountID int64) (*Account, error) {
	return nil, nil
}

func (c *recordingSchedulerCache) SetAccount(ctx context.Context, account *Account) error {
	return nil
}

func (c *recordingSchedulerCache) DeleteAccount(ctx context.Context, accountID int64) error {
	return nil
}

func (c *recordingSchedulerCache) UpdateLastUsed(ctx context.Context, updates map[int64]time.Time) error {
	return nil
}

func (c *recordingSchedulerCache) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.locks = append(c.locks, bucket)
	return true, nil
}

func (c *recordingSchedulerCache) UnlockBucket(ctx context.Context, bucket SchedulerBucket) error {
	return nil
}

func (c *recordingSchedulerCache) ListBuckets(ctx context.Context) ([]SchedulerBucket, error) {
	return nil, nil
}

func (c *recordingSchedulerCache) GetOutboxWatermark(ctx context.Context) (int64, error) {
	return 0, nil
}

func (c *recordingSchedulerCache) SetOutboxWatermark(ctx context.Context, id int64) error {
	return nil
}

// stubAccountRepoForRebuild implements AccountRepository with empty results so
// rebuildBucket reaches SetSnapshot but does not depend on real DB data.
type stubAccountRepoForRebuild struct {
	AccountRepository // embed for the methods we don't exercise — they will panic if hit
}

func (r *stubAccountRepoForRebuild) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	return nil, nil
}

func (r *stubAccountRepoForRebuild) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]Account, error) {
	return nil, nil
}

// TestRebuildByGroupIDs_RebuildsNewAPIBucket is the behavioral regression
// guard for Bug A. Before the fix, rebuildByGroupIDs hardcoded the 4
// pre-newapi platforms — calling it on a group_change event would silently
// skip the newapi bucket, leaving it stale until the next periodic full
// rebuild (see docs/approved/newapi-as-fifth-platform.md).
//
// This test calls rebuildByGroupIDs and asserts that the (groupID, "newapi",
// single) and (groupID, "newapi", forced) buckets were attempted to be
// rebuilt. To prove the test actually catches the regression: removing
// PlatformNewAPI from AllSchedulingPlatforms() must make this test fail.
func TestRebuildByGroupIDs_RebuildsNewAPIBucket(t *testing.T) {
	cache := &recordingSchedulerCache{}
	accountRepo := &stubAccountRepoForRebuild{}
	svc := NewSchedulerSnapshotService(cache, nil, accountRepo, nil, nil)

	groupID := int64(42)
	if err := svc.rebuildByGroupIDs(context.Background(), []int64{groupID}, "test", nil); err != nil {
		t.Fatalf("rebuildByGroupIDs returned error: %v", err)
	}

	// Verify each canonical scheduling platform got at least one bucket
	// rebuild attempt for this group. The Mixed mode is platform-specific
	// (anthropic + gemini only) — we only assert Single + Forced presence,
	// which every scheduling platform must have.
	want := map[string]struct{ single, forced bool }{
		PlatformAnthropic:   {},
		PlatformGemini:      {},
		PlatformOpenAI:      {},
		PlatformAntigravity: {},
		PlatformNewAPI:      {},
	}
	for _, b := range cache.locks {
		if b.GroupID != groupID {
			continue
		}
		if _, ok := want[b.Platform]; !ok {
			continue
		}
		entry := want[b.Platform]
		switch b.Mode {
		case SchedulerModeSingle:
			entry.single = true
		case SchedulerModeForced:
			entry.forced = true
		}
		want[b.Platform] = entry
	}

	for p, modes := range want {
		if !modes.single {
			t.Errorf("rebuildByGroupIDs did not rebuild (%d, %q, single) — Bug A regression", groupID, p)
		}
		if !modes.forced {
			t.Errorf("rebuildByGroupIDs did not rebuild (%d, %q, forced) — Bug A regression", groupID, p)
		}
	}
}

// TestDefaultBuckets_IncludesNewAPI guards the same canonical-list contract
// at app-startup / cold-snapshot time. Before Bug A's fix, defaultBuckets
// also enumerated only the 4 legacy platforms, so a fresh-process restart
// without any persisted newapi bucket left the newapi cold snapshot empty.
func TestDefaultBuckets_IncludesNewAPI(t *testing.T) {
	svc := NewSchedulerSnapshotService(nil, nil, nil, nil, nil)
	buckets, err := svc.defaultBuckets(context.Background())
	if err != nil {
		t.Fatalf("defaultBuckets returned error: %v", err)
	}

	want := map[string]struct{ single, forced bool }{
		PlatformAnthropic:   {},
		PlatformGemini:      {},
		PlatformOpenAI:      {},
		PlatformAntigravity: {},
		PlatformNewAPI:      {},
	}
	for _, b := range buckets {
		if b.GroupID != 0 { // we only care about the platform-default seed buckets
			continue
		}
		if _, ok := want[b.Platform]; !ok {
			continue
		}
		entry := want[b.Platform]
		switch b.Mode {
		case SchedulerModeSingle:
			entry.single = true
		case SchedulerModeForced:
			entry.forced = true
		}
		want[b.Platform] = entry
	}

	for p, modes := range want {
		if !modes.single {
			t.Errorf("defaultBuckets missing (0, %q, single) — Bug A regression", p)
		}
		if !modes.forced {
			t.Errorf("defaultBuckets missing (0, %q, forced) — Bug A regression", p)
		}
	}
}
