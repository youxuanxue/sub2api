//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"

	"github.com/stretchr/testify/require"
)

// --- stubs -------------------------------------------------------------------

type reconcilerAccountStub struct {
	accounts   []Account            // default list (back-compat for existing tests)
	byPlatform map[string][]Account // optional per-platform override
	sum        int64
	bulkCalls  []bulkUpdateCall
	listErr    error
	sumErr     error
	bulkErr    error
}

type bulkUpdateCall struct {
	ids     []int64
	updates AccountBulkUpdate
}

func (s *reconcilerAccountStub) ListByPlatform(ctx context.Context, platform string) ([]Account, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.byPlatform != nil {
		return s.byPlatform[platform], nil // absent platform → empty list
	}
	if platform == PlatformAnthropic {
		return s.accounts, nil
	}
	return nil, nil
}

func (s *reconcilerAccountStub) SumConcurrencyAnthropic(ctx context.Context) (int64, error) {
	if s.sumErr != nil {
		return 0, s.sumErr
	}
	return s.sum, nil
}

func (s *reconcilerAccountStub) BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	if s.bulkErr != nil {
		return 0, s.bulkErr
	}
	s.bulkCalls = append(s.bulkCalls, bulkUpdateCall{ids: ids, updates: updates})
	return int64(len(ids)), nil
}

type reconcilerUserStub struct {
	user           *User
	setConcurrency []int
	getErr         error
}

func (s *reconcilerUserStub) GetByID(ctx context.Context, id int64) (*User, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.user, nil
}

func (s *reconcilerUserStub) BatchSetConcurrency(ctx context.Context, userIDs []int64, value int) (int, error) {
	s.setConcurrency = append(s.setConcurrency, value)
	if s.user != nil {
		s.user.Concurrency = value // reflect for verify read-back
	}
	return len(userIDs), nil
}

func newTestReconciler(acc *reconcilerAccountStub, usr *reconcilerUserStub, cfg *config.Config) *AnthropicConfigReconciler {
	r := NewAnthropicConfigReconciler(acc, usr, nil, nil, nil, nil, cfg, nil)
	return r
}

// --- Step baseline (account shared_baseline self-heal) stubs + tests ----------

type stubTierApplier struct {
	calls []int64 // account ids RepairBaselineDrift was invoked on
}

func (s *stubTierApplier) RepairBaselineDrift(ctx context.Context, accountID int64, tier string) (*Account, error) {
	s.calls = append(s.calls, accountID)
	return &Account{ID: accountID}, nil
}

type stubTLSByID struct {
	byID map[int64]*model.TLSFingerprintProfile
}

func (s *stubTLSByID) GetByID(ctx context.Context, id int64) (*model.TLSFingerprintProfile, error) {
	return s.byID[id], nil
}

func baselineSelfHealReconciler(applier *stubTierApplier, tls *stubTLSByID) *AnthropicConfigReconciler {
	tiers := &stubTierConcurrency{names: map[int64]string{2: "l2"}} // tier_id 2 → "l2"
	return NewAnthropicConfigReconciler(nil, nil, tiers, applier, tls, nil, nil, nil)
}

func tierID2() *int64 { v := int64(2); return &v }

func TestReconcileAccountBaselineDrift_HealsUnboundTLS(t *testing.T) {
	applier := &stubTierApplier{}
	r := baselineSelfHealReconciler(applier, &stubTLSByID{})
	// tier-bound oauth account with priority=1 but NO tls binding → drift.
	accts := []Account{{
		ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, TierID: tierID2(),
		Priority: 1,
		Extra:    map[string]any{"enable_tls_fingerprint": true},
	}}
	r.reconcileAccountBaselineDrift(context.Background(), accts)
	require.Equal(t, []int64{7}, applier.calls, "unbound TLS must trigger narrow baseline repair")
}

func TestReconcileAccountBaselineDrift_IgnoresPriorityDrift(t *testing.T) {
	applier := &stubTierApplier{}
	tls := &stubTLSByID{byID: map[int64]*model.TLSFingerprintProfile{5: {ID: 5, Name: "tk_canonical_cc_oauth"}}}
	r := baselineSelfHealReconciler(applier, tls)
	// fully bound infra + credentials present, but priority=2 (≠ baseline 1).
	// priority is owned by the window-rebalance pipeline at runtime, so a drifted
	// priority on an otherwise-aligned account must NOT trigger self-heal —
	// otherwise the reconciler would flatten rebalance's window-aware ordering.
	accts := []Account{{
		ID: 8, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, TierID: tierID2(),
		Priority:    2,
		Extra:       map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_profile_id": int64(5)},
		Credentials: map[string]any{"temp_unschedulable_enabled": true, "temp_unschedulable_rules": []any{}, "intercept_warmup_requests": true},
	}}
	r.reconcileAccountBaselineDrift(context.Background(), accts)
	require.Empty(t, applier.calls, "priority drift alone must NOT trigger self-heal (rebalance owns runtime priority)")
}

func TestReconcileAccountBaselineDrift_SkipsAligned(t *testing.T) {
	applier := &stubTierApplier{}
	tls := &stubTLSByID{byID: map[int64]*model.TLSFingerprintProfile{5: {ID: 5, Name: "tk_canonical_cc_oauth"}}}
	r := baselineSelfHealReconciler(applier, tls)
	// fully aligned: priority=1, bound to canonical row, flag on, creds template present.
	accts := []Account{{
		ID: 9, Platform: PlatformAnthropic, Type: AccountTypeOAuth, TierID: tierID2(),
		Priority:    1,
		Extra:       map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_profile_id": int64(5)},
		Credentials: map[string]any{"temp_unschedulable_enabled": true, "temp_unschedulable_rules": []any{}, "intercept_warmup_requests": true},
	}}
	r.reconcileAccountBaselineDrift(context.Background(), accts)
	require.Empty(t, applier.calls, "aligned account must NOT be re-applied (skip-if-aligned)")
}

func TestReconcileAccountBaselineDrift_HealsDanglingBinding(t *testing.T) {
	applier := &stubTierApplier{}
	// id 5 bound but the profile row does not exist (deleted) → dangling.
	r := baselineSelfHealReconciler(applier, &stubTLSByID{byID: map[int64]*model.TLSFingerprintProfile{}})
	accts := []Account{{
		ID: 10, Platform: PlatformAnthropic, Type: AccountTypeOAuth, TierID: tierID2(),
		Priority:    1,
		Extra:       map[string]any{"enable_tls_fingerprint": true, "tls_fingerprint_profile_id": int64(5)},
		Credentials: map[string]any{"temp_unschedulable_enabled": true, "temp_unschedulable_rules": []any{}, "intercept_warmup_requests": true},
	}}
	r.reconcileAccountBaselineDrift(context.Background(), accts)
	require.Equal(t, []int64{10}, applier.calls, "dangling TLS binding must trigger ApplyTier self-heal")
}

func TestReconcileAccountBaselineDrift_SkipsApikeyStub(t *testing.T) {
	applier := &stubTierApplier{}
	r := baselineSelfHealReconciler(applier, &stubTLSByID{})
	accts := []Account{{
		ID: 11, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, TierID: tierID2(), Priority: 9,
	}}
	r.reconcileAccountBaselineDrift(context.Background(), accts)
	require.Empty(t, applier.calls, "apikey stub must be skipped")
}

func TestReconcileAccountBaselineDrift_SkipsPassthrough(t *testing.T) {
	applier := &stubTierApplier{}
	r := baselineSelfHealReconciler(applier, &stubTLSByID{})
	accts := []Account{{
		ID:       12,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		TierID:   tierID2(),
		Extra: map[string]any{
			"anthropic_oauth_passthrough": true,
		},
	}}
	r.reconcileAccountBaselineDrift(context.Background(), accts)
	require.Empty(t, applier.calls, "passthrough accounts must not receive canonical TLS baseline repair")
}

// stubTierConcurrency is a minimal reconcilerTierResolver for Step T tests.
type stubTierConcurrency struct {
	conc  map[int64]int
	names map[int64]string
}

func (s *stubTierConcurrency) ResolveConcurrency(tierID int64) (int, bool) {
	v, ok := s.conc[tierID]
	return v, ok
}

func (s *stubTierConcurrency) ResolveName(tierID int64) (string, bool) {
	v, ok := s.names[tierID]
	return v, ok
}

func reconcilerTestCfg() *config.Config {
	c := &config.Config{}
	c.Gateway.Scheduling.AnthropicConfigReconcilerIntervalSeconds = 300
	return c
}

// --- tests -------------------------------------------------------------------

func TestReconciler_OperatorConcurrencySelfHeal(t *testing.T) {
	acc := &reconcilerAccountStub{sum: 24}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 8}}
	r := newTestReconciler(acc, usr, reconcilerTestCfg())

	r.runOnce(context.Background())

	require.Equal(t, []int{24}, usr.setConcurrency, "operator concurrency must be set to Σ schedulable anthropic")
}

func TestReconciler_OperatorConcurrencyAlreadyAligned_NoWrite(t *testing.T) {
	acc := &reconcilerAccountStub{sum: 8}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 8}}
	r := newTestReconciler(acc, usr, reconcilerTestCfg())

	r.runOnce(context.Background())

	require.Empty(t, usr.setConcurrency, "no write when operator concurrency already equals Σ")
}

func TestReconciler_TierDrift_ReportOnly_NoWrite(t *testing.T) {
	// l4 baseline is concurrency=10; this account drifted to 2 but is an OAUTH
	// account (not a stub), so the reconciler must only report, never write.
	drifted := Account{
		ID:          41,
		Name:        "prod-oauth-l4",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 2, // drift vs l4 baseline (10)
		Priority:    4,
		Extra: map[string]any{
			AccountTierExtraKey: "l4",
			"base_rpm":          float64(28),
			"max_sessions":      float64(120),
		},
	}
	acc := &reconcilerAccountStub{accounts: []Account{drifted}, sum: 2}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 2}}
	r := newTestReconciler(acc, usr, reconcilerTestCfg())

	r.runOnce(context.Background())

	require.Empty(t, acc.bulkCalls, "tier drift must be report-only — never rewrite the account")
}
