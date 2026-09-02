//go:build unit

package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAccountCompatibleWithGroupPlatform(t *testing.T) {
	t.Parallel()

	require.NoError(t, accountCompatibleWithGroupPlatform(PlatformOpenAI, PlatformOpenAI, false))
	require.Error(t, accountCompatibleWithGroupPlatform(PlatformOpenAI, PlatformAnthropic, false))

	require.Error(t, accountCompatibleWithGroupPlatform(PlatformAnthropic, PlatformAntigravity, false))
	require.NoError(t, accountCompatibleWithGroupPlatform(PlatformAnthropic, PlatformAntigravity, true))
	require.NoError(t, accountCompatibleWithGroupPlatform(PlatformGemini, PlatformAntigravity, true))

	require.NoError(t, accountCompatibleWithGroupPlatform(PlatformComposite, PlatformOpenAI, false))
	require.NoError(t, accountCompatibleWithGroupPlatform(PlatformComposite, PlatformNewAPI, false))
	require.Error(t, accountCompatibleWithGroupPlatform(PlatformComposite, PlatformComposite, false))
}

type membershipAccountRepoFake struct {
	accountRepoStubForAdminList
	byID map[int64]*Account
}

func (r *membershipAccountRepoFake) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	out := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if acc, ok := r.byID[id]; ok {
			out = append(out, acc)
		}
	}
	return out, nil
}

func (r *membershipAccountRepoFake) ListByGroup(context.Context, int64) ([]Account, error) {
	return nil, nil
}

func TestBindGroupAccounts_SamePlatformOK(t *testing.T) {
	var bound []int64
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			1: {ID: 1, Name: "openai", Platform: PlatformOpenAI},
		},
		bindAccountsToGroupFn: func(_ int64, accountIDs []int64) error {
			bound = append([]int64(nil), accountIDs...)
			return nil
		},
	}
	accRepo := &membershipAccountRepoFake{byID: map[int64]*Account{
		10: {ID: 10, Name: "a", Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	}}
	svc := &adminServiceImpl{groupRepo: groupRepo, accountRepo: accRepo}

	require.NoError(t, svc.BindGroupAccounts(context.Background(), 1, []int64{10}, false))
	require.Equal(t, []int64{10}, bound)
}

func TestBindGroupAccounts_RejectsCrossPlatform(t *testing.T) {
	bound := false
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			1: {ID: 1, Name: "openai", Platform: PlatformOpenAI},
		},
		bindAccountsToGroupFn: func(int64, []int64) error {
			bound = true
			return nil
		},
	}
	accRepo := &membershipAccountRepoFake{byID: map[int64]*Account{
		10: {ID: 10, Name: "a", Platform: PlatformAnthropic, Type: AccountTypeOAuth},
	}}
	svc := &adminServiceImpl{groupRepo: groupRepo, accountRepo: accRepo}

	err := svc.BindGroupAccounts(context.Background(), 1, []int64{10}, false)
	require.Error(t, err)
	require.Equal(t, "GROUP_ACCOUNT_PLATFORM_MISMATCH", infraerrors.Reason(err))
	require.False(t, bound)
}

func TestBindGroupAccounts_AllowsAntigravityMixedOnAnthropicGroup(t *testing.T) {
	var bound []int64
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			1: {ID: 1, Name: "claude", Platform: PlatformAnthropic},
		},
		bindAccountsToGroupFn: func(_ int64, accountIDs []int64) error {
			bound = append([]int64(nil), accountIDs...)
			return nil
		},
	}
	accRepo := &membershipAccountRepoFake{byID: map[int64]*Account{
		10: {
			ID: 10, Name: "ag", Platform: PlatformAntigravity, Type: AccountTypeOAuth,
			Extra: map[string]any{"mixed_scheduling": true},
		},
	}}
	svc := &adminServiceImpl{groupRepo: groupRepo, accountRepo: accRepo}

	require.NoError(t, svc.BindGroupAccounts(context.Background(), 1, []int64{10}, true))
	require.Equal(t, []int64{10}, bound)
}

func TestUnbindGroupAccounts_RemovesMembership(t *testing.T) {
	var unbound []int64
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			1: {ID: 1, Name: "openai", Platform: PlatformOpenAI},
		},
	}
	// Override via embedding replacement: patch Unbind on a thin wrapper.
	svc := &adminServiceImpl{groupRepo: &unbindCaptureGroupRepo{
		groupRepoStubForAdmin: *groupRepo,
		unbound:               &unbound,
	}}

	require.NoError(t, svc.UnbindGroupAccounts(context.Background(), 1, []int64{10, 11}))
	require.Equal(t, []int64{10, 11}, unbound)
}

type unbindCaptureGroupRepo struct {
	groupRepoStubForAdmin
	unbound *[]int64
}

func (r *unbindCaptureGroupRepo) UnbindAccountsFromGroup(_ context.Context, _ int64, accountIDs []int64) error {
	*r.unbound = append([]int64(nil), accountIDs...)
	return nil
}

func TestBindGroupAccounts_RejectsMissingAccount(t *testing.T) {
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			1: {ID: 1, Name: "openai", Platform: PlatformOpenAI},
		},
		bindAccountsToGroupFn: func(int64, []int64) error { return nil },
	}
	accRepo := &membershipAccountRepoFake{byID: map[int64]*Account{}}
	svc := &adminServiceImpl{groupRepo: groupRepo, accountRepo: accRepo}

	err := svc.BindGroupAccounts(context.Background(), 1, []int64{99}, false)
	require.Error(t, err)
	require.Equal(t, "ACCOUNT_NOT_FOUND", infraerrors.Reason(err))
}

func TestBindGroupAccounts_RejectsPublicAggregatorAsBadRequest(t *testing.T) {
	bound := false
	groupRepo := &groupRepoStubForAdmin{
		getByIDByID: map[int64]*Group{
			1: {ID: 1, Name: "public-newapi", Platform: PlatformNewAPI, IsExclusive: false},
		},
		bindAccountsToGroupFn: func(int64, []int64) error {
			bound = true
			return nil
		},
	}
	accRepo := &membershipAccountRepoFake{byID: map[int64]*Account{
		10: {
			ID: 10, Name: "or", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
			ChannelType: newapiconstant.ChannelTypeOpenRouter,
		},
	}}
	svc := &adminServiceImpl{groupRepo: groupRepo, accountRepo: accRepo}

	err := svc.BindGroupAccounts(context.Background(), 1, []int64{10}, true)
	require.Error(t, err)
	require.Equal(t, "PUBLIC_GROUP_AGGREGATOR_CHANNEL", infraerrors.Reason(err))
	require.Equal(t, 400, infraerrors.Code(err))
	require.False(t, bound)
}
