package service

import (
	"context"
)

// tkPrepareBulkUpdateExtras applies TokenKey bulk-update Extra guards and
// upstream-billing probe eligibility checks against the hydrated target set.
func (s *adminServiceImpl) tkPrepareBulkUpdateExtras(input *BulkUpdateAccountsInput, cachedTargets []*Account) error {
	hasManaged := false
	for _, account := range cachedTargets {
		if IsSupplierManagedAccount(account) {
			hasManaged = true
			break
		}
	}
	if hasManaged {
		input.Extra = StripSupplierReservedAccountExtra(input.Extra)
	} else if err := ValidateSupplierReservedAccountExtra(input.Extra); err != nil {
		return err
	}

	if input.ProbeEnabled != nil {
		targetsByID := make(map[int64]*Account, len(cachedTargets))
		for _, account := range cachedTargets {
			if account != nil {
				targetsByID[account.ID] = account
			}
		}
		for _, accountID := range input.AccountIDs {
			account, ok := targetsByID[accountID]
			if !ok {
				return ErrAccountNotFound
			}
			if !isUpstreamBillingProbeAccount(account) {
				return ErrUpstreamBillingProbeAccountInvalid
			}
			if *input.ProbeEnabled && !upstreamBillingProbeSupportsSub2APIBilling(account) {
				return ErrUpstreamBillingProbeAccountInvalid
			}
		}
	}
	return nil
}

// tkBulkUpdatePublicGroupChecks enforces scheme-C public-group aggregator policy
// before any bulk write that rebinds groups.
func (s *adminServiceImpl) tkBulkUpdatePublicGroupChecks(ctx context.Context, input *BulkUpdateAccountsInput, accountByID map[int64]*Account) error {
	if input.GroupIDs == nil {
		return nil
	}
	for _, accountID := range input.AccountIDs {
		account := accountByID[accountID]
		if account == nil {
			continue
		}
		if err := s.checkPublicGroupAggregatorChannelPolicy(ctx, account.ID, account.Name, account.Platform, account.ChannelType, account.Credentials, *input.GroupIDs); err != nil {
			return err
		}
	}
	return nil
}

// tkFinalizeBulkUpdateAccounts runs TokenKey post-bulk side effects.
func (s *adminServiceImpl) tkFinalizeBulkUpdateAccounts(ctx context.Context, result *BulkUpdateAccountsResult) error {
	if result.Success > 0 && s.userRepo != nil {
		if err := SyncAnthropicOperatorConcurrency(ctx, s.accountRepo, s.userRepo); err != nil {
			return err
		}
	}
	return nil
}
