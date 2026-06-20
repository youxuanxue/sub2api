//go:build unit

package service

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// --- lightweight stubs (embed the interfaces, override only what's exercised) ---

type gcapAccountRepoBatch struct {
	AccountRepository
	byGroup map[int64][]Account
}

func (r *gcapAccountRepoBatch) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]Account, error) {
	return append([]Account(nil), r.byGroup[groupID]...), nil
}

func (r *gcapAccountRepoBatch) ListSchedulableByGroupIDs(_ context.Context, groupIDs []int64) (map[int64][]Account, error) {
	out := make(map[int64][]Account, len(groupIDs))
	for _, gid := range groupIDs {
		if accs, ok := r.byGroup[gid]; ok && len(accs) > 0 {
			out[gid] = append([]Account(nil), accs...)
		}
	}
	return out, nil
}

// gcapAccountRepoPerGroup deliberately does NOT implement ListSchedulableByGroupIDs,
// so the type-assertion in getAllGroupCapacityBatched fails and GetAllGroupCapacity
// falls back to its original per-group loop.
type gcapAccountRepoPerGroup struct {
	AccountRepository
	byGroup map[int64][]Account
}

func (r *gcapAccountRepoPerGroup) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]Account, error) {
	return append([]Account(nil), r.byGroup[groupID]...), nil
}

type gcapGroupRepo struct {
	GroupRepository
	groups []Group
}

func (r *gcapGroupRepo) ListActive(_ context.Context) ([]Group, error) {
	return append([]Group(nil), r.groups...), nil
}

type gcapConcCache struct {
	ConcurrencyCache
	counts map[int64]int
}

func (c *gcapConcCache) GetAccountConcurrencyBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = c.counts[id]
	}
	return out, nil
}

type gcapSessCache struct {
	SessionLimitCache
	counts map[int64]int
}

func (c *gcapSessCache) GetActiveSessionCountBatch(_ context.Context, ids []int64, _ map[int64]time.Duration) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = c.counts[id]
	}
	return out, nil
}

type gcapRPMCache struct {
	RPMCache
	counts map[int64]int
}

func (c *gcapRPMCache) GetRPMBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(ids))
	for _, id := range ids {
		out[id] = c.counts[id]
	}
	return out, nil
}

// TestGetAllGroupCapacity_BatchEqualsPerGroup proves the batched fast path emits
// the byte-identical []GroupCapacitySummary the original per-group loop produces,
// and pins the expected aggregation (including a zero-account group, a group whose
// rpm_limit overrides Σ base_rpm, and an account shared across two groups).
func TestGetAllGroupCapacity_BatchEqualsPerGroup(t *testing.T) {
	a1 := Account{ID: 1, Concurrency: 4, Extra: map[string]any{"max_sessions": 10, "base_rpm": 30, "session_idle_timeout_minutes": 3}}
	a2 := Account{ID: 2, Concurrency: 2, Extra: map[string]any{"max_sessions": 5}}
	a3 := Account{ID: 3, Concurrency: 1} // no sessions, no base_rpm

	byGroup := map[int64][]Account{
		1: {a1, a2}, // two accounts
		2: {a3},     // rpm_limit override applies (a3 has no base_rpm)
		3: {},       // empty -> zero-capacity summary, still emitted
		4: {a1},     // a1 shared with group 1 (multi-membership)
	}
	groups := []Group{{ID: 1}, {ID: 2, RPMLimit: 100}, {ID: 3}, {ID: 4}}

	concCounts := map[int64]int{1: 2, 2: 1, 3: 0}
	sessCounts := map[int64]int{1: 3, 2: 4}
	rpmCounts := map[int64]int{1: 7, 2: 0, 3: 5}

	newSvc := func(repo AccountRepository) *GroupCapacityService {
		return NewGroupCapacityService(
			repo,
			&gcapGroupRepo{groups: groups},
			NewConcurrencyService(&gcapConcCache{counts: concCounts}),
			&gcapSessCache{counts: sessCounts},
			&gcapRPMCache{counts: rpmCounts},
		)
	}

	batchSvc := newSvc(&gcapAccountRepoBatch{byGroup: byGroup})
	perGroupSvc := newSvc(&gcapAccountRepoPerGroup{byGroup: byGroup})

	got, err := batchSvc.GetAllGroupCapacity(context.Background())
	if err != nil {
		t.Fatalf("batch path error: %v", err)
	}
	fallback, err := perGroupSvc.GetAllGroupCapacity(context.Background())
	if err != nil {
		t.Fatalf("fallback path error: %v", err)
	}

	if !reflect.DeepEqual(got, fallback) {
		t.Fatalf("batch path != per-group fallback\nbatch=%+v\nfallback=%+v", got, fallback)
	}

	want := []GroupCapacitySummary{
		{GroupID: 1, ConcurrencyUsed: 3, ConcurrencyMax: 6, SessionsUsed: 7, SessionsMax: 15, RPMUsed: 7, RPMMax: 30},
		{GroupID: 2, ConcurrencyUsed: 0, ConcurrencyMax: 1, SessionsUsed: 0, SessionsMax: 0, RPMUsed: 5, RPMMax: 100},
		{GroupID: 3},
		{GroupID: 4, ConcurrencyUsed: 2, ConcurrencyMax: 4, SessionsUsed: 3, SessionsMax: 10, RPMUsed: 7, RPMMax: 30},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected capacity\n got=%+v\nwant=%+v", got, want)
	}
}
