//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"

	"github.com/stretchr/testify/require"
)

func tierStrPtr(s string) *string { return &s }

// stubAdminServiceForTier captures UpdateAccount input + returns a fixed account.
type stubAdminServiceForTier struct {
	AdminService
	getAccount    *Account
	updateInput   *UpdateAccountInput
	updateReturns *Account
}

func (s *stubAdminServiceForTier) GetAccount(ctx context.Context, id int64) (*Account, error) {
	return s.getAccount, nil
}

func (s *stubAdminServiceForTier) UpdateAccount(ctx context.Context, id int64, input *UpdateAccountInput) (*Account, error) {
	s.updateInput = input
	if s.updateReturns != nil {
		return s.updateReturns, nil
	}
	return &Account{ID: id}, nil
}

// stubTierRepo returns a fixed set of tiers (keyed by name) for TierService.
type stubTierRepo struct {
	byName map[string]*model.Tier
}

func (r *stubTierRepo) List(ctx context.Context) ([]*model.Tier, error) {
	out := make([]*model.Tier, 0, len(r.byName))
	for _, t := range r.byName {
		out = append(out, t)
	}
	return out, nil
}

func (r *stubTierRepo) GetByID(ctx context.Context, id int64) (*model.Tier, error) {
	for _, t := range r.byName {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, nil
}

func (r *stubTierRepo) GetByName(ctx context.Context, name string) (*model.Tier, error) {
	return r.byName[name], nil
}

func (r *stubTierRepo) Create(ctx context.Context, t *model.Tier) (*model.Tier, error) { return t, nil }
func (r *stubTierRepo) Update(ctx context.Context, t *model.Tier) (*model.Tier, error) { return t, nil }
func (r *stubTierRepo) Delete(ctx context.Context, id int64) error                     { return nil }

func (r *stubTierRepo) UpsertByName(ctx context.Context, t *model.Tier) (*model.Tier, error) {
	if r.byName == nil {
		r.byName = map[string]*model.Tier{}
	}
	// Preserve a pre-seeded explicit row (e.g. the l4 fixture) instead of
	// clobbering it with the baseline seed.
	if _, ok := r.byName[t.Name]; !ok {
		r.byName[t.Name] = t
	}
	return r.byName[t.Name], nil
}

func tierServiceWithL4(t *testing.T) *TierService {
	t.Helper()
	repo := &stubTierRepo{byName: map[string]*model.Tier{
		"l4": {
			ID: 4, Name: "l4", Concurrency: 8, Priority: 4, RateMultiplier: 1.0,
			BaseRPM: 28, MaxSessions: 120, RPMStickyBuffer: 20, WindowCostLimit: 600,
			TLSProfileName: tierStrPtr("tk_canonical_cc_oauth"),
		},
	}}
	return NewTierService(repo, nil)
}

func TestApplyTier_RejectsNonAnthropic(t *testing.T) {
	svc := NewAccountTierService(
		&stubAdminServiceForTier{getAccount: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
		tierServiceWithL4(t), nil)
	_, err := svc.ApplyTier(context.Background(), 1, "l4")
	require.Error(t, err)
}

func TestApplyTier_RejectsApikeyStub(t *testing.T) {
	// applying a tier to an anthropic apikey mirror stub must be rejected (it
	// would otherwise wipe base_url/pool_mode).
	svc := NewAccountTierService(
		&stubAdminServiceForTier{getAccount: &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}},
		tierServiceWithL4(t), nil)
	_, err := svc.ApplyTier(context.Background(), 1, "l4")
	require.Error(t, err)
}

func TestApplyTier_SetupTokenBindsTier(t *testing.T) {
	// Regression: setup-token anthropic accounts are subject to the same 5h
	// window + session control as OAuth (Account.IsAnthropicOAuthOrSetupToken)
	// and MUST be tier-eligible. PR #472's over-narrow Type==oauth gate rejected
	// them; this asserts they now bind.
	admin := &stubAdminServiceForTier{getAccount: &Account{
		ID:       9,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
	}}
	svc := NewAccountTierService(admin, tierServiceWithL4(t), nil)

	_, err := svc.ApplyTier(context.Background(), 9, "l4")
	require.NoError(t, err)
	require.NotNil(t, admin.updateInput)
	require.NotNil(t, admin.updateInput.TierID)
	require.Equal(t, int64(4), *admin.updateInput.TierID, "setup-token must bind tier_id")
	require.NotNil(t, admin.updateInput.Concurrency)
	require.Equal(t, 8, *admin.updateInput.Concurrency, "setup-token must value-sync concurrency from tier")
}

func TestApplyTierExtra_OverlaysSetupToken(t *testing.T) {
	// Runtime overlay must apply to setup-token accounts too, otherwise a bound
	// setup-token account would read base_rpm/max_sessions == 0 (unlimited).
	tierID := int64(4)
	acct := &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken, TierID: &tierID}
	tierServiceWithL4(t).ApplyTierExtra(acct)
	require.Equal(t, 28, parseExtraInt(acct.Extra["base_rpm"]), "setup-token overlay must set base_rpm from tier")
	require.Equal(t, 120, parseExtraInt(acct.Extra["max_sessions"]), "setup-token overlay must set max_sessions from tier")
}

func TestTierManagedExtraStripped_StripsSetupTokenOverlay(t *testing.T) {
	// Write path must strip overlaid tier-managed keys for setup-token too, so the
	// in-memory overlay is never persisted back onto the account.
	tierID := int64(4)
	acct := &Account{
		Platform: PlatformAnthropic, Type: AccountTypeSetupToken, TierID: &tierID,
		Extra: map[string]any{"base_rpm": 28, "rpm_strategy": "tiered"},
	}
	out := tierServiceWithL4(t).TierManagedExtraStripped(acct)
	_, hasBaseRPM := out["base_rpm"]
	require.False(t, hasBaseRPM, "tier-managed base_rpm must be stripped for setup-token write path")
	require.Equal(t, "tiered", out["rpm_strategy"], "non-tier extra preserved")
}

func TestApplyTier_OAuthBindsTierAndValueSyncsConcurrency(t *testing.T) {
	admin := &stubAdminServiceForTier{getAccount: &Account{
		ID:       7,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		// stale tier-managed extra that must NOT be persisted back.
		Extra: map[string]any{"base_rpm": 999, "rpm_strategy": "tiered"},
	}}
	svc := NewAccountTierService(admin, tierServiceWithL4(t), nil)

	_, err := svc.ApplyTier(context.Background(), 7, "l4")
	require.NoError(t, err)
	require.NotNil(t, admin.updateInput)

	in := admin.updateInput
	require.NotNil(t, in.TierID)
	require.Equal(t, int64(4), *in.TierID, "must bind tier_id")
	require.NotNil(t, in.Concurrency)
	require.Equal(t, 8, *in.Concurrency, "must value-sync concurrency from tier")
	require.Nil(t, in.Priority, "must NOT write priority (window pipeline owns it)")

	require.Equal(t, "l4", in.Extra["stability_tier"])
	require.Equal(t, true, in.Extra["enable_tls_fingerprint"])
	require.Equal(t, "tiered", in.Extra["rpm_strategy"], "non-tier extra preserved")
	_, hasBaseRPM := in.Extra["base_rpm"]
	require.False(t, hasBaseRPM, "tier-managed numeric extra must NOT be copied onto the account")
}
