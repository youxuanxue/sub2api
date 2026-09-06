package service

import (
	"context"
	"fmt"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// SupplierAnthropicRoutingGroupName is the TokenKey scheduling group that every
// Anthropic-channel (channel_type=14) supplier-managed account must join so
// claude-keyed API traffic can select it. Name is the SSOT; id is resolved at runtime.
const SupplierAnthropicRoutingGroupName = "claude"

type accountGroupEnsurer interface {
	EnsureAccountGroups(ctx context.Context, accountID int64, groupIDs []int64) error
}

func supplierNeedsAnthropicRoutingGroup(channelType int) bool {
	return channelType == newapiconstant.ChannelTypeAnthropic
}

func resolveSupplierAnthropicRoutingGroupID(ctx context.Context, groups GroupRepository) (int64, error) {
	if groups == nil {
		return 0, fmt.Errorf("%w: group repository unavailable", ErrSupplierSourceInvalidInput)
	}
	active, err := groups.ListActiveByPlatform(ctx, PlatformAnthropic)
	if err != nil {
		return 0, err
	}
	for _, group := range active {
		if group.Name == SupplierAnthropicRoutingGroupName {
			return group.ID, nil
		}
	}
	return 0, fmt.Errorf(
		"%w: routing group %q for platform %s not found",
		ErrSupplierSourceInvalidInput,
		SupplierAnthropicRoutingGroupName,
		PlatformAnthropic,
	)
}

func (s *adminServiceImpl) supplierAnthropicRoutingGroupIDs(ctx context.Context, channelType int) ([]int64, error) {
	if !supplierNeedsAnthropicRoutingGroup(channelType) {
		return nil, nil
	}
	groupID, err := resolveSupplierAnthropicRoutingGroupID(ctx, s.groupRepo)
	if err != nil {
		return nil, err
	}
	return []int64{groupID}, nil
}

func (s *adminServiceImpl) EnsureSupplierRoutingGroups(
	ctx context.Context,
	accountID int64,
	channelType int,
) error {
	if s == nil || accountID <= 0 {
		return ErrSupplierSourceInvalidInput
	}
	groupIDs, err := s.supplierAnthropicRoutingGroupIDs(ctx, channelType)
	if err != nil {
		return err
	}
	if len(groupIDs) == 0 {
		return nil
	}
	ensurer, ok := s.accountRepo.(accountGroupEnsurer)
	if !ok {
		return ErrSupplierProjectionUpdaterMissing
	}
	return ensurer.EnsureAccountGroups(ctx, accountID, groupIDs)
}
