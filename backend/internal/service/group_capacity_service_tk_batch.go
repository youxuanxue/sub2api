package service

import (
	"context"
	"time"
)

// schedulableGroupBatchRepo is the optional batch capability the real
// AccountRepository (repository.accountRepository) implements. The capacity
// service type-asserts its accountRepo for it so the fast path activates in
// production while test stubs (which only implement the per-group method) fall
// back to the original loop in GetAllGroupCapacity. This keeps the perf rewrite
// entirely in TK companion files — no change to the AccountRepository interface
// and therefore no churn across its mocks.
type schedulableGroupBatchRepo interface {
	ListSchedulableByGroupIDs(ctx context.Context, groupIDs []int64) (map[int64][]Account, error)
}

// getAllGroupCapacityBatched collapses GetAllGroupCapacity's per-group N+1
// (~4 DB + ~3 Redis round-trips PER active group, all serial) into a constant
// number of round-trips: one batched account load for every group, then a
// single GetAccountConcurrencyBatch / GetActiveSessionCountBatch / GetRPMBatch
// over the de-duplicated union of all accounts, folded back per group.
//
// It returns ok=false when the underlying repo does not implement the batch
// method, so GetAllGroupCapacity falls through to its original per-group loop.
// When ok=true the result (and any error) is authoritative.
//
// Output is byte-identical to the per-group path:
//   - groups with no schedulable accounts still emit a zero-capacity summary
//     (the old getGroupCapacity returned GroupCapacitySummary{} + nil, which the
//     caller appended with GroupID set);
//   - per-account concurrency/session/RPM values are independent of which group
//     queried them, so a unioned batch yields the same numbers as N per-group
//     batches;
//   - sessions are only summed for groups whose Σ max_sessions > 0, and RPM only
//     for groups with Σ base_rpm > 0 OR a group-level rpm_limit, exactly as
//     getGroupCapacity gated them;
//   - rpm_max is overridden by group.rpm_limit when that L1 ceiling is set.
func (s *GroupCapacityService) getAllGroupCapacityBatched(ctx context.Context) ([]GroupCapacitySummary, bool, error) {
	batchRepo, ok := s.accountRepo.(schedulableGroupBatchRepo)
	if !ok {
		return nil, false, nil
	}

	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, true, err
	}
	if len(groups) == 0 {
		return []GroupCapacitySummary{}, true, nil
	}

	groupIDs := make([]int64, len(groups))
	for i := range groups {
		groupIDs[i] = groups[i].ID
	}

	accountsByGroup, err := batchRepo.ListSchedulableByGroupIDs(ctx, groupIDs)
	if err != nil {
		return nil, true, err
	}

	// Union of all account IDs (dedup) + per-account session idle timeout, so the
	// three Redis batch calls run once across the whole set instead of per group.
	unionIDs := make([]int64, 0, len(accountsByGroup))
	unionSeen := make(map[int64]struct{}, len(accountsByGroup))
	sessionTimeouts := make(map[int64]time.Duration)
	for _, accounts := range accountsByGroup {
		for i := range accounts {
			acc := &accounts[i]
			if _, dup := unionSeen[acc.ID]; !dup {
				unionSeen[acc.ID] = struct{}{}
				unionIDs = append(unionIDs, acc.ID)
			}
			if ms := acc.GetMaxSessions(); ms > 0 {
				timeout := time.Duration(acc.GetSessionIdleTimeoutMinutes()) * time.Minute
				if timeout <= 0 {
					timeout = 5 * time.Minute
				}
				sessionTimeouts[acc.ID] = timeout
			}
		}
	}

	concurrencyMap, _ := s.concurrencyService.GetAccountConcurrencyBatch(ctx, unionIDs)

	var sessionsMap map[int64]int
	if len(sessionTimeouts) > 0 && s.sessionLimitCache != nil {
		sessionsMap, _ = s.sessionLimitCache.GetActiveSessionCountBatch(ctx, unionIDs, sessionTimeouts)
	}

	var rpmMap map[int64]int
	if s.rpmCache != nil {
		rpmMap, _ = s.rpmCache.GetRPMBatch(ctx, unionIDs)
	}

	results := make([]GroupCapacitySummary, 0, len(groups))
	for i := range groups {
		g := &groups[i]
		summary := GroupCapacitySummary{GroupID: g.ID}
		accounts := accountsByGroup[g.ID]
		if len(accounts) == 0 {
			results = append(results, summary)
			continue
		}

		var concurrencyMax, sessionsMax, rpmMax int
		var concurrencyUsed int
		for j := range accounts {
			acc := &accounts[j]
			concurrencyMax += acc.Concurrency
			concurrencyUsed += concurrencyMap[acc.ID]
			if ms := acc.GetMaxSessions(); ms > 0 {
				sessionsMax += ms
			}
			if rpm := acc.GetBaseRPM(); rpm > 0 {
				rpmMax += rpm
			}
		}

		var sessionsUsed int
		if sessionsMax > 0 {
			for j := range accounts {
				sessionsUsed += sessionsMap[accounts[j].ID]
			}
		}

		var rpmUsed int
		if rpmMax > 0 || g.RPMLimit > 0 {
			for j := range accounts {
				rpmUsed += rpmMap[accounts[j].ID]
			}
		}

		// group.rpm_limit (L1 gate) wins over Σ base_rpm when set — see the
		// matching note in getGroupCapacity.
		if g.RPMLimit > 0 {
			rpmMax = g.RPMLimit
		}

		summary.ConcurrencyUsed = concurrencyUsed
		summary.ConcurrencyMax = concurrencyMax
		summary.SessionsUsed = sessionsUsed
		summary.SessionsMax = sessionsMax
		summary.RPMUsed = rpmUsed
		summary.RPMMax = rpmMax
		results = append(results, summary)
	}

	return results, true, nil
}
