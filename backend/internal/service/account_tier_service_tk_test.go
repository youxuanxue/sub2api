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

// stubTLSRepo is an in-memory TLSFingerprintProfileRepository for unit tests.
// It lets us exercise GetOrUpsertByName (create-if-absent / update-in-place)
// without a DB. Create assigns a monotonic id; Update replaces by id.
type stubTLSRepo struct {
	rows   []*model.TLSFingerprintProfile
	nextID int64
	create int // number of Create calls (asserts "profile was materialized")
	update int // number of Update calls (asserts idempotent re-apply)
}

func (r *stubTLSRepo) List(ctx context.Context) ([]*model.TLSFingerprintProfile, error) {
	return r.rows, nil
}

func (r *stubTLSRepo) GetByID(ctx context.Context, id int64) (*model.TLSFingerprintProfile, error) {
	for _, p := range r.rows {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, nil
}

func (r *stubTLSRepo) Create(ctx context.Context, p *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
	r.create++
	r.nextID++
	p.ID = r.nextID
	r.rows = append(r.rows, p)
	return p, nil
}

func (r *stubTLSRepo) Update(ctx context.Context, p *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
	r.update++
	for i, x := range r.rows {
		if x.ID == p.ID {
			r.rows[i] = p
			return p, nil
		}
	}
	return p, nil
}

func (r *stubTLSRepo) Delete(ctx context.Context, id int64) error { return nil }

// tlsServiceWithRepo builds a real TLSFingerprintProfileService over the given
// stub repo (nil cache — invalidateAndNotify is nil-cache safe).
func tlsServiceWithRepo(t *testing.T, repo *stubTLSRepo) *TLSFingerprintProfileService {
	t.Helper()
	return NewTLSFingerprintProfileService(repo, nil)
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
	svc := NewAccountTierService(admin, tierServiceWithL4(t), tlsServiceWithRepo(t, &stubTLSRepo{}))

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
	svc := NewAccountTierService(admin, tierServiceWithL4(t), tlsServiceWithRepo(t, &stubTLSRepo{}))

	_, err := svc.ApplyTier(context.Background(), 7, "l4")
	require.NoError(t, err)
	require.NotNil(t, admin.updateInput)

	in := admin.updateInput
	require.NotNil(t, in.TierID)
	require.Equal(t, int64(4), *in.TierID, "must bind tier_id")
	require.NotNil(t, in.Concurrency)
	require.Equal(t, 8, *in.Concurrency, "must value-sync concurrency from tier")
	// post-#551: ApplyTier value-syncs priority to the baseline (uniform = 1).
	require.NotNil(t, in.Priority, "must write priority (post-#551 uniform baseline)")
	require.Equal(t, 1, *in.Priority, "priority value-synced to baseline")

	require.Equal(t, "l4", in.Extra["stability_tier"])
	require.Equal(t, true, in.Extra["enable_tls_fingerprint"])
	require.Equal(t, "tiered", in.Extra["rpm_strategy"], "non-tier extra preserved")
	_, hasBaseRPM := in.Extra["base_rpm"]
	require.False(t, hasBaseRPM, "tier-managed numeric extra must NOT be copied onto the account")
}

// TestApplyTier_CreatesAndBindsCanonicalTLSProfile is the regression for the
// reported bug: operator added an OAuth account + set tier, but the
// tls_fingerprint_profiles table had no tk_canonical_cc_oauth row, so the
// account ran on the built-in default ClientHello. ApplyTier must now CREATE the
// canonical profile (GetOrUpsertByName) and bind the account to its id.
func TestApplyTier_CreatesAndBindsCanonicalTLSProfile(t *testing.T) {
	admin := &stubAdminServiceForTier{getAccount: &Account{
		ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
	}}
	tlsRepo := &stubTLSRepo{} // empty — no canonical profile exists yet
	svc := NewAccountTierService(admin, tierServiceWithL4(t), tlsServiceWithRepo(t, tlsRepo))

	_, err := svc.ApplyTier(context.Background(), 7, "l4")
	require.NoError(t, err)
	require.Equal(t, 1, tlsRepo.create, "canonical TLS profile row must be CREATED when absent")
	require.Len(t, tlsRepo.rows, 1)
	require.Equal(t, "tk_canonical_cc_oauth", tlsRepo.rows[0].Name)

	in := admin.updateInput
	require.NotNil(t, in)
	// the account must be bound to the freshly-created profile id (not 0).
	boundID := parseExtraInt(in.Extra["tls_fingerprint_profile_id"])
	require.Equal(t, int(tlsRepo.rows[0].ID), boundID, "account must bind the canonical profile id")
	require.NotZero(t, boundID, "binding must not be empty (the root-cause bug)")
	require.Equal(t, true, in.Extra["enable_tls_fingerprint"])
}

// TestApplyTier_WritesCredentialsTemplate asserts the 403 self-protection /
// warmup credentials template is materialized, and OAuth tokens are preserved.
func TestApplyTier_WritesCredentialsTemplate(t *testing.T) {
	admin := &stubAdminServiceForTier{getAccount: &Account{
		ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "tok-abc", "refresh_token": "ref-xyz"},
	}}
	svc := NewAccountTierService(admin, tierServiceWithL4(t), tlsServiceWithRepo(t, &stubTLSRepo{}))

	_, err := svc.ApplyTier(context.Background(), 7, "l4")
	require.NoError(t, err)
	in := admin.updateInput
	require.NotNil(t, in.Credentials)
	require.Equal(t, true, in.Credentials["temp_unschedulable_enabled"], "self-protection template written")
	require.NotNil(t, in.Credentials["temp_unschedulable_rules"], "403 disable rules written")
	require.Equal(t, true, in.Credentials["intercept_warmup_requests"], "warmup intercept written")
	// existing OAuth tokens carried through (also protected by MergePreservingSensitiveCreds).
	require.Equal(t, "tok-abc", in.Credentials["access_token"], "OAuth access_token preserved")
	require.Equal(t, "ref-xyz", in.Credentials["refresh_token"], "OAuth refresh_token preserved")
}

// TestApplyTier_Idempotent re-applies the same tier: the second call must UPDATE
// the existing canonical profile in place (not create a duplicate), keeping the
// same bound id — this is the property the reconciler self-heal relies on.
func TestApplyTier_Idempotent(t *testing.T) {
	admin := &stubAdminServiceForTier{getAccount: &Account{
		ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
	}}
	tlsRepo := &stubTLSRepo{}
	svc := NewAccountTierService(admin, tierServiceWithL4(t), tlsServiceWithRepo(t, tlsRepo))

	_, err := svc.ApplyTier(context.Background(), 7, "l4")
	require.NoError(t, err)
	firstID := parseExtraInt(admin.updateInput.Extra["tls_fingerprint_profile_id"])

	_, err = svc.ApplyTier(context.Background(), 7, "l4")
	require.NoError(t, err)
	secondID := parseExtraInt(admin.updateInput.Extra["tls_fingerprint_profile_id"])

	require.Equal(t, 1, tlsRepo.create, "second apply must NOT create a duplicate profile")
	require.GreaterOrEqual(t, tlsRepo.update, 1, "second apply updates the existing profile in place")
	require.Equal(t, firstID, secondID, "bound profile id is stable across re-apply")
	require.Len(t, tlsRepo.rows, 1, "exactly one canonical profile row")
}

// TestApplyTier_FailsLoudWithoutTLSService: an unbound canonical profile is the
// bug we are eliminating — a missing TLS service must be a hard error, not a warn.
func TestApplyTier_FailsLoudWithoutTLSService(t *testing.T) {
	admin := &stubAdminServiceForTier{getAccount: &Account{
		ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
	}}
	svc := NewAccountTierService(admin, tierServiceWithL4(t), nil)
	_, err := svc.ApplyTier(context.Background(), 7, "l4")
	require.Error(t, err, "missing TLS service must fail loud (no silent built-in fallback)")
	require.Nil(t, admin.updateInput, "must not write the account when the binding can't be ensured")
}
