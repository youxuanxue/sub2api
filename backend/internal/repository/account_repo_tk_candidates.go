package repository

import (
	"context"
	"sort"
	"time"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListCandidateAccounts loads the authorized membership union in one query.
// Disabled / unschedulable members remain in the snapshot so inference selection
// can separate support from readiness; discovery menus then apply
// Account.IsLiveForDiscovery (docs/approved/discovery-require-live-account.md)
// before advertising a model.
func (r *accountRepository) ListCandidateAccounts(ctx context.Context, groupIDs []int64) ([]service.Account, error) {
	if len(groupIDs) == 0 {
		return []service.Account{}, nil
	}
	accounts, err := clientFromContext(ctx, r.client).Account.Query().Where(
		dbaccount.HasAccountGroupsWith(dbaccountgroup.GroupIDIn(groupIDs...)),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	return r.accountsToService(ctx, accounts)
}

// BuildCandidateReadSnapshot is the bounded repository fallback for the
// candidate read-model provider. It hydrates the requested membership union in
// batch and projects it through the service-owned credential-free builder;
// credentials never enter the returned snapshot.
func (r *accountRepository) BuildCandidateReadSnapshot(ctx context.Context, groupIDs []int64) (service.CandidateReadSnapshot, error) {
	accounts, err := r.ListCandidateAccounts(ctx, groupIDs)
	if err != nil {
		return service.CandidateReadSnapshot{}, err
	}
	allGroupIDs := append([]int64(nil), groupIDs...)
	for _, account := range accounts {
		allGroupIDs = append(allGroupIDs, account.GroupIDs...)
	}
	groupsByID, err := r.loadGroups(ctx, allGroupIDs)
	if err != nil {
		return service.CandidateReadSnapshot{}, err
	}
	groups := make([]service.Group, 0, len(groupsByID))
	for _, group := range groupsByID {
		if group != nil {
			groups = append(groups, *group)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	// Repository fallbacks are request-scoped reads. A timestamp gives the
	// materializer a monotonic local generation without pretending this path is
	// the scheduler's durable outbox watermark.
	revision := uint64(time.Now().UnixNano())
	return service.BuildCandidateReadSnapshot(accounts, groups, revision, revision, time.Now().UTC())
}
