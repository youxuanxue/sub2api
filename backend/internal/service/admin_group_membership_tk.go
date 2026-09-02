package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// accountCompatibleWithGroupPlatform enforces the membership platform policy:
// concrete groups require same platform (anthropic/gemini may also take
// antigravity accounts with mixed_scheduling); composite accepts any concrete
// scheduling platform. This is the SSOT used by group-side bind — keep it
// aligned with GroupSelector + gateway mixed scheduling.
func accountCompatibleWithGroupPlatform(groupPlatform, accountPlatform string, mixedScheduling bool) error {
	groupPlatform = strings.TrimSpace(groupPlatform)
	accountPlatform = strings.TrimSpace(accountPlatform)
	if accountPlatform == "" {
		return infraerrors.BadRequest("GROUP_ACCOUNT_PLATFORM_REQUIRED", "account platform is required")
	}
	if groupPlatform == PlatformComposite {
		if accountPlatform == PlatformComposite {
			return infraerrors.BadRequest(
				"GROUP_ACCOUNT_PLATFORM_MISMATCH",
				"composite groups cannot bind composite-platform accounts",
			)
		}
		return nil
	}
	if accountPlatform == groupPlatform {
		return nil
	}
	if (groupPlatform == PlatformAnthropic || groupPlatform == PlatformGemini) &&
		accountPlatform == PlatformAntigravity && mixedScheduling {
		return nil
	}
	return infraerrors.Newf(
		http.StatusBadRequest,
		"GROUP_ACCOUNT_PLATFORM_MISMATCH",
		"account platform %q is not compatible with group platform %q",
		accountPlatform,
		groupPlatform,
	)
}

// ListGroupAccounts returns paginated accounts bound to the group via account_groups.
func (s *adminServiceImpl) ListGroupAccounts(
	ctx context.Context,
	groupID int64,
	page, pageSize int,
	status, search string,
	channelType int,
) ([]Account, int64, error) {
	if _, err := s.groupRepo.GetByIDLite(ctx, groupID); err != nil {
		return nil, 0, err
	}
	return s.ListAccounts(ctx, page, pageSize, "", "", status, search, groupID, "", "name", "asc", channelType)
}

// BindGroupAccounts adds accounts to a group. Writes only account_groups.
func (s *adminServiceImpl) BindGroupAccounts(
	ctx context.Context,
	groupID int64,
	accountIDs []int64,
	skipMixedChannelCheck bool,
) error {
	group, err := s.groupRepo.GetByID(ctx, groupID)
	if err != nil {
		return err
	}
	if group == nil {
		return ErrGroupNotFound
	}

	uniqueIDs := uniquePositiveInt64s(accountIDs)
	if len(uniqueIDs) == 0 {
		return infraerrors.BadRequest("GROUP_ACCOUNT_IDS_REQUIRED", "account_ids is required")
	}

	accounts, err := s.accountRepo.GetByIDs(ctx, uniqueIDs)
	if err != nil {
		return fmt.Errorf("load accounts: %w", err)
	}
	found := make(map[int64]*Account, len(accounts))
	for _, acc := range accounts {
		if acc != nil {
			found[acc.ID] = acc
		}
	}
	for _, id := range uniqueIDs {
		if _, ok := found[id]; !ok {
			return infraerrors.Newf(http.StatusNotFound, "ACCOUNT_NOT_FOUND", "account %d not found", id)
		}
	}

	for _, id := range uniqueIDs {
		acc := found[id]
		if err := accountCompatibleWithGroupPlatform(group.Platform, acc.Platform, acc.IsMixedSchedulingEnabled()); err != nil {
			return err
		}
		if group.RequireOAuthOnly && groupSupportsOAuthOnlyFilter(group.Platform) && acc.Type == AccountTypeAPIKey {
			return infraerrors.Newf(
				http.StatusBadRequest,
				"GROUP_OAUTH_ONLY",
				"group %q only allows OAuth accounts; account %d is apikey",
				group.Name,
				acc.ID,
			)
		}
	}

	if !skipMixedChannelCheck {
		for _, id := range uniqueIDs {
			acc := found[id]
			if err := s.checkMixedChannelRisk(ctx, acc.ID, acc.Platform, []int64{groupID}); err != nil {
				return err
			}
		}
	}

	for _, id := range uniqueIDs {
		acc := found[id]
		if err := s.checkPublicGroupAggregatorChannelPolicy(
			ctx,
			acc.ID,
			acc.Name,
			acc.Platform,
			acc.ChannelType,
			acc.Credentials,
			[]int64{groupID},
		); err != nil {
			return err
		}
	}

	return s.groupRepo.BindAccountsToGroup(ctx, groupID, uniqueIDs)
}

// UnbindGroupAccounts removes membership edges for the given accounts in one group.
func (s *adminServiceImpl) UnbindGroupAccounts(ctx context.Context, groupID int64, accountIDs []int64) error {
	if _, err := s.groupRepo.GetByIDLite(ctx, groupID); err != nil {
		return err
	}
	uniqueIDs := uniquePositiveInt64s(accountIDs)
	if len(uniqueIDs) == 0 {
		return infraerrors.BadRequest("GROUP_ACCOUNT_IDS_REQUIRED", "account_ids is required")
	}
	return s.groupRepo.UnbindAccountsFromGroup(ctx, groupID, uniqueIDs)
}

func uniquePositiveInt64s(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
