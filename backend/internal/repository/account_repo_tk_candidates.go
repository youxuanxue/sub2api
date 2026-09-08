package repository

import (
	"context"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListCandidateAccounts loads the authorized membership union in one query.
// Disabled members remain visible so support is not mistaken for live capacity.
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
