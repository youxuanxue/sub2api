//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsAccountAllowedForPlatform_KiroMixedAnthropicOnly(t *testing.T) {
	svc := &GatewayService{}
	mixed := &Account{Platform: PlatformKiro, Extra: map[string]any{"mixed_scheduling": true}}
	plain := &Account{Platform: PlatformKiro}

	require.True(t, svc.isAccountAllowedForPlatform(mixed, PlatformAnthropic, true))
	require.False(t, svc.isAccountAllowedForPlatform(plain, PlatformAnthropic, true))
	require.False(t, svc.isAccountAllowedForPlatform(mixed, PlatformGemini, true))
	require.False(t, svc.isAccountAllowedForPlatform(mixed, PlatformAnthropic, false))
}

func TestSelectAccountWithMixedScheduling_KiroOAuthWithFlag(t *testing.T) {
	ctx := context.Background()
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{
				ID:          303,
				Platform:    PlatformKiro,
				Type:        AccountTypeOAuth,
				Priority:    1,
				Status:      StatusActive,
				Schedulable: true,
				Extra:       map[string]any{"mixed_scheduling": true},
				Credentials: map[string]any{},
			},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &GatewayService{
		accountRepo: repo,
		cache:       &mockGatewayCacheForPlatform{},
		cfg:         testConfig(),
	}
	acc, err := svc.selectAccountWithMixedScheduling(ctx, nil, "", "claude-sonnet-4-6", nil, PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(303), acc.ID)
	require.Equal(t, PlatformKiro, acc.Platform)
}

func TestSelectAccountWithMixedScheduling_FiltersKiroWithoutFlag(t *testing.T) {
	ctx := context.Background()
	repo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Priority: 2, Status: StatusActive, Schedulable: true},
			{ID: 3, Platform: PlatformKiro, Priority: 1, Status: StatusActive, Schedulable: true}, // no mixed flag
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range repo.accounts {
		repo.accountsByID[repo.accounts[i].ID] = &repo.accounts[i]
	}
	svc := &GatewayService{
		accountRepo: repo,
		cache:       &mockGatewayCacheForPlatform{},
		cfg:         testConfig(),
	}
	acc, err := svc.selectAccountWithMixedScheduling(ctx, nil, "", "claude-sonnet-4-6", nil, PlatformAnthropic)
	require.NoError(t, err)
	require.NotNil(t, acc)
	require.Equal(t, int64(1), acc.ID, "kiro without mixed_scheduling must be filtered out")
}
