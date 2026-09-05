//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ProbeEnabled eligibility must run AFTER OpenAI settings validation so a
// dual-failure BulkUpdate prefers OPENAI_BULK_TARGET_INVALID (pre-companion
// order). A Kiro OAuth account fails both gates.
func TestBulkUpdateAccounts_OpenAISettingsBeforeProbeEnabled(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{
		{ID: 1, Platform: PlatformKiro, Type: AccountTypeOAuth},
	}}
	svc := &adminServiceImpl{accountRepo: repo}
	enabled := true

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs:   []int64{1},
		ProbeEnabled: &enabled,
		Extra:        map[string]any{openAILongContextBillingEnabledKey: true},
	})

	require.Error(t, err)
	requireApplicationErrorReason(t, err, "OPENAI_BULK_TARGET_INVALID")
	require.False(t, errors.Is(err, ErrUpstreamBillingProbeAccountInvalid),
		"probe must not win over OpenAI settings when both would fail")
	require.Zero(t, repo.bulkUpdateCalls)
}

func TestTkValidateBulkProbeEnabled_RejectsNonProbeAccount(t *testing.T) {
	svc := &adminServiceImpl{}
	enabled := true
	err := svc.tkValidateBulkProbeEnabled(&BulkUpdateAccountsInput{
		AccountIDs:   []int64{9},
		ProbeEnabled: &enabled,
	}, map[int64]*Account{
		9: {ID: 9, Platform: PlatformKiro, Type: AccountTypeOAuth},
	})
	require.ErrorIs(t, err, ErrUpstreamBillingProbeAccountInvalid)
}

func TestTkValidateBulkProbeEnabled_AcceptsProbeAccount(t *testing.T) {
	svc := &adminServiceImpl{}
	enabled := true
	err := svc.tkValidateBulkProbeEnabled(&BulkUpdateAccountsInput{
		AccountIDs:   []int64{3},
		ProbeEnabled: &enabled,
	}, map[int64]*Account{
		3: {ID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
	})
	require.NoError(t, err)
}

func TestTkPrepareBulkUpdateExtras_DoesNotValidateProbe(t *testing.T) {
	svc := &adminServiceImpl{}
	enabled := true
	input := &BulkUpdateAccountsInput{
		AccountIDs:   []int64{1},
		ProbeEnabled: &enabled,
		Extra:        map[string]any{"note": "ok"},
	}
	// Kiro OAuth would fail probe validation — prepare must ignore ProbeEnabled.
	err := svc.tkPrepareBulkUpdateExtras(input, []*Account{
		{ID: 1, Platform: PlatformKiro, Type: AccountTypeOAuth},
	})
	require.NoError(t, err)
}
