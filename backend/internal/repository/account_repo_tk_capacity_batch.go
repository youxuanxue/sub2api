package repository

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListSchedulableByGroupIDs is the batched sibling of ListSchedulableByGroupID.
//
// TK perf: GroupCapacityService.GetAllGroupCapacity used to call
// ListSchedulableByGroupID once per active group — and each call fans out into
// queryAccountsByGroup + accountsToService (loadProxies + loadAccountGroups),
// i.e. ~4 serial DB round-trips per group. With dozens of active groups the
// admin Groups page paid hundreds of serialized round-trips on every open /
// refresh / filter change. This method loads the schedulable accounts for ALL
// requested groups in one AccountGroup query (WithAccount eager-load), then
// enriches the de-duplicated union of accounts via a single accountsToService
// call, so the whole capacity summary costs ~3 DB round-trips regardless of
// group count.
//
// Semantics are identical to calling ListSchedulableByGroupID per group: the
// same account predicates (active + schedulable + not temp-unschedulable + not
// expired + not overloaded + not rate-limited), the same priority ordering, the
// same per-account enrichment. A group with no schedulable accounts is simply
// absent from the returned map (the caller treats a missing/empty slice as a
// zero-capacity group, matching the old per-group len(accounts)==0 branch).
func (r *accountRepository) ListSchedulableByGroupIDs(ctx context.Context, groupIDs []int64) (map[int64][]service.Account, error) {
	result := make(map[int64][]service.Account, len(groupIDs))
	if len(groupIDs) == 0 {
		return result, nil
	}

	now := time.Now()
	rows, err := r.client.AccountGroup.Query().
		Where(
			dbaccountgroup.GroupIDIn(groupIDs...),
			// Mirror queryAccountsByGroup's schedulable predicate set exactly.
			dbaccountgroup.HasAccountWith(
				dbaccount.DeletedAtIsNil(),
				dbaccount.StatusEQ(domain.StatusActive),
				dbaccount.SchedulableEQ(true),
				tempUnschedulablePredicate(),
				notExpiredPredicate(now),
				dbaccount.Or(dbaccount.OverloadUntilIsNil(), dbaccount.OverloadUntilLTE(now)),
				dbaccount.Or(dbaccount.RateLimitResetAtIsNil(), dbaccount.RateLimitResetAtLTE(now)),
			),
		).
		Order(
			dbaccountgroup.ByPriority(),
			dbaccountgroup.ByAccountField(dbaccount.FieldPriority),
		).
		WithAccount().
		All(ctx)
	if err != nil {
		return nil, err
	}

	// Build per-group ordered unique account-ID lists and the de-duplicated
	// union of underlying ent accounts (so accountsToService enriches each
	// account exactly once, even when it belongs to multiple requested groups).
	orderedIDsByGroup := make(map[int64][]int64, len(groupIDs))
	perGroupSeen := make(map[int64]map[int64]struct{}, len(groupIDs))
	unionAccounts := make([]*dbent.Account, 0, len(rows))
	unionSeen := make(map[int64]struct{}, len(rows))
	for _, ag := range rows {
		if ag.Edges.Account == nil {
			continue
		}
		seen := perGroupSeen[ag.GroupID]
		if seen == nil {
			seen = make(map[int64]struct{})
			perGroupSeen[ag.GroupID] = seen
		}
		if _, dup := seen[ag.AccountID]; !dup {
			seen[ag.AccountID] = struct{}{}
			orderedIDsByGroup[ag.GroupID] = append(orderedIDsByGroup[ag.GroupID], ag.AccountID)
		}
		if _, dup := unionSeen[ag.AccountID]; !dup {
			unionSeen[ag.AccountID] = struct{}{}
			unionAccounts = append(unionAccounts, ag.Edges.Account)
		}
	}

	svcAccounts, err := r.accountsToService(ctx, unionAccounts)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]service.Account, len(svcAccounts))
	for _, acc := range svcAccounts {
		byID[acc.ID] = acc
	}

	for gid, ids := range orderedIDsByGroup {
		accs := make([]service.Account, 0, len(ids))
		for _, id := range ids {
			if acc, ok := byID[id]; ok {
				accs = append(accs, acc)
			}
		}
		result[gid] = accs
	}
	return result, nil
}
