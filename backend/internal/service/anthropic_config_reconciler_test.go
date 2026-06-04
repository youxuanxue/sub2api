//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"

	"github.com/stretchr/testify/require"
)

// --- stubs -------------------------------------------------------------------

type reconcilerAccountStub struct {
	accounts   []Account            // default list (back-compat for existing tests)
	byPlatform map[string][]Account // optional per-platform override (runOnce now lists anthropic AND kiro)
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
	return s.accounts, nil
}

func (s *reconcilerAccountStub) SumConcurrencyAnthropic(ctx context.Context) (int64, error) {
	if s.sumErr != nil {
		return 0, s.sumErr
	}
	return s.sum, nil
}

func (s *reconcilerAccountStub) SumConcurrencyAnthropicByGroup(context.Context, string) (int64, error) {
	if s.sumErr != nil {
		return 0, s.sumErr
	}
	return s.sum, nil
}

func (s *reconcilerAccountStub) SumConcurrencyByPlatform(context.Context, string) (int64, error) {
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

type reconcilerBalanceStub struct {
	setCalls []float64
}

func (s *reconcilerBalanceStub) UpdateUserBalance(ctx context.Context, userID int64, balance float64, operation string, notes string) (*User, error) {
	s.setCalls = append(s.setCalls, balance)
	return &User{ID: userID, Balance: balance}, nil
}

type fakeDoer struct {
	status   int
	body     string
	err      error
	lastURL  string
	lastAuth string
}

func (d *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	d.lastURL = req.URL.String()
	d.lastAuth = req.Header.Get("x-api-key")
	if d.err != nil {
		return nil, d.err
	}
	return &http.Response{
		StatusCode: d.status,
		Body:       io.NopCloser(bytes.NewBufferString(d.body)),
		Header:     make(http.Header),
	}, nil
}

func newTestReconciler(acc *reconcilerAccountStub, usr *reconcilerUserStub, bal *reconcilerBalanceStub, cfg *config.Config) *AnthropicConfigReconciler {
	r := NewAnthropicConfigReconciler(acc, usr, bal, nil, nil, nil, nil, cfg, nil)
	return r
}

// --- Step baseline (account shared_baseline self-heal) stubs + tests ----------

type stubTierApplier struct {
	calls []int64 // account ids ReapplyBaselineInfra was invoked on
}

func (s *stubTierApplier) ReapplyBaselineInfra(ctx context.Context, accountID int64, tier string) (*Account, error) {
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
	return NewAnthropicConfigReconciler(nil, nil, nil, tiers, applier, tls, nil, nil, nil)
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
	require.Equal(t, []int64{7}, applier.calls, "unbound TLS must trigger ApplyTier self-heal")
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
	// apikey mirror stub is not OAuth/setup-token → never re-applied.
	accts := []Account{{
		ID: 11, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, TierID: tierID2(), Priority: 9,
	}}
	r.reconcileAccountBaselineDrift(context.Background(), accts)
	require.Empty(t, applier.calls, "apikey stub must be skipped")
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

func mirrorEnabledCfg(mirror, balanceFloor bool) *config.Config {
	c := &config.Config{}
	c.Gateway.Scheduling.AnthropicConfigReconcilerIntervalSeconds = 300
	c.Gateway.Scheduling.AnthropicConfigReconcilerConcurrencyMirrorEnabled = mirror
	c.Gateway.Scheduling.AnthropicConfigReconcilerBalanceFloorEnabled = balanceFloor
	return c
}

// --- tests -------------------------------------------------------------------

func TestReconciler_OperatorConcurrencySelfHeal(t *testing.T) {
	acc := &reconcilerAccountStub{sum: 24}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 8}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(false, false))

	r.runOnce(context.Background())

	require.Equal(t, []int{24}, usr.setConcurrency, "operator concurrency must be set to Σ schedulable anthropic")
}

func TestReconciler_OperatorConcurrencyAlreadyAligned_NoWrite(t *testing.T) {
	acc := &reconcilerAccountStub{sum: 8}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 8}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(false, false))

	r.runOnce(context.Background())

	require.Empty(t, usr.setConcurrency, "no write when operator concurrency already equals Σ")
}

func TestReconciler_StubPoolModeSelfHeal(t *testing.T) {
	stub := Account{
		ID:       11,
		Name:     "prod-mirror-us1",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api-us1.tokenkey.dev",
			"api_key":  "sk-edge-key",
			// pool_mode absent → IsPoolMode()==false → drift vs policy(true)
		},
	}
	acc := &reconcilerAccountStub{accounts: []Account{stub}, sum: 0}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 0}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(false, false))

	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1, "pool_mode drift must trigger exactly one credentials write")
	call := acc.bulkCalls[0]
	require.Equal(t, []int64{11}, call.ids)
	require.Equal(t, true, call.updates.Credentials["pool_mode"])
	require.Equal(t, 3, call.updates.Credentials["pool_mode_retry_count"])
	require.Nil(t, call.updates.Concurrency, "pool_mode self-heal must not touch concurrency")
}

func TestReconciler_NonMirrorStub_Ignored(t *testing.T) {
	// anthropic oauth account (not an apikey mirror stub) must be left alone.
	oauth := Account{
		ID:          12,
		Name:        "edge-oauth-1",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 8,
		Credentials: map[string]any{"access_token": "x"},
	}
	acc := &reconcilerAccountStub{accounts: []Account{oauth}, sum: 8}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 8}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(true, false))

	r.runOnce(context.Background())

	require.Empty(t, acc.bulkCalls, "non-stub anthropic accounts must never be written by the reconciler")
}

func TestReconciler_SurfaceC_MirrorsConcurrency(t *testing.T) {
	stub := Account{
		ID:          21,
		Name:        "prod-mirror-us1",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 8,
		Credentials: map[string]any{
			"base_url":              "https://api-us1.tokenkey.dev",
			"api_key":               "sk-edge-key",
			"pool_mode":             true,
			"pool_mode_retry_count": 3,
		},
	}
	acc := &reconcilerAccountStub{accounts: []Account{stub}, sum: 8}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 8}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(true, false))
	doer := &fakeDoer{status: 200, body: `{"code":0,"message":"ok","data":{"platform":"anthropic","total_concurrency":5,"ts":1}}`}
	r.http = doer

	r.runOnce(context.Background())

	// exactly one concurrency write to 5
	var concWrites int
	for _, call := range acc.bulkCalls {
		if call.updates.Concurrency != nil {
			concWrites++
			require.Equal(t, 5, *call.updates.Concurrency)
			require.Equal(t, []int64{21}, call.ids)
		}
	}
	require.Equal(t, 1, concWrites, "surface-C must mirror edge total_concurrency onto the stub")
	require.Contains(t, doer.lastURL, "/api/v1/edge/scheduling-capacity")
	require.Contains(t, doer.lastURL, "platform=anthropic")
	require.Equal(t, "sk-edge-key", doer.lastAuth, "must authenticate with the stub's relay api_key")
}

// A kiro mirror stub rides the same anthropic-apikey transport but declares
// credentials.mirror_platform=kiro, so surface-C must query the edge's kiro pool
// (?platform=kiro) — not the anthropic pool. This is the bug the fix closes: both
// stubs otherwise read the same anthropic number (22).
func TestReconciler_SurfaceC_KiroStub_QueriesKiroPlatform(t *testing.T) {
	stub := Account{
		ID:          22,
		Name:        "kiro-edge-us1",
		Platform:    PlatformAnthropic, // transport platform is always anthropic-apikey
		Type:        AccountTypeAPIKey,
		Concurrency: 22, // wrong (anthropic) number it currently shows
		Credentials: map[string]any{
			"base_url":        "https://api-us1.tokenkey.dev",
			"api_key":         "sk-edge-key",
			"mirror_platform": "kiro",
		},
	}
	acc := &reconcilerAccountStub{accounts: []Account{stub}, sum: 22}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 22}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(true, false))
	doer := &fakeDoer{status: 200, body: `{"code":0,"message":"ok","data":{"platform":"kiro","total_concurrency":6,"ts":1}}`}
	r.http = doer

	r.runOnce(context.Background())

	require.Contains(t, doer.lastURL, "platform=kiro", "kiro stub must query the edge kiro pool, not anthropic")
	require.NotContains(t, doer.lastURL, "platform=anthropic")

	var concWrites int
	for _, call := range acc.bulkCalls {
		if call.updates.Concurrency != nil {
			concWrites++
			require.Equal(t, 6, *call.updates.Concurrency, "kiro stub must mirror the edge kiro Σ (6), not anthropic (22)")
			require.Equal(t, []int64{22}, call.ids)
		}
	}
	require.Equal(t, 1, concWrites)
}

// mirrorCapacityPlatform: only empty/whitespace defaults to anthropic; any
// non-empty value is passed through verbatim (lower/trimmed) so an unknown/typo'd
// value reaches the edge, 4xx's, and is skipped — never silently coerced to the
// anthropic pool (which would reintroduce the silent-wrong-pool bug).
func TestMirrorCapacityPlatform(t *testing.T) {
	cases := map[string]string{
		"":          "anthropic",
		"   ":       "anthropic",
		"anthropic": "anthropic",
		"Anthropic": "anthropic",
		"kiro":      "kiro",
		" KIRO ":    "kiro",
		"openai":    "openai", // unsupported today → passthrough, edge will 4xx
		"kir0":      "kir0",   // typo → passthrough, NOT coerced to anthropic
	}
	for raw, want := range cases {
		if got := mirrorCapacityPlatform(raw); got != want {
			t.Errorf("mirrorCapacityPlatform(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Absent credentials.mirror_platform, a stub keeps mirroring the anthropic pool —
// every pre-existing stub stays correct with zero data migration.
func TestReconciler_SurfaceC_DefaultsToAnthropicWhenUnset(t *testing.T) {
	stub := Account{
		ID:          23,
		Name:        "cc-us1",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 8,
		Credentials: map[string]any{
			"base_url": "https://api-us1.tokenkey.dev",
			"api_key":  "sk-edge-key",
		},
	}
	acc := &reconcilerAccountStub{accounts: []Account{stub}, sum: 8}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 8}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(true, false))
	doer := &fakeDoer{status: 200, body: `{"code":0,"message":"ok","data":{"platform":"anthropic","total_concurrency":5,"ts":1}}`}
	r.http = doer

	r.runOnce(context.Background())

	require.Contains(t, doer.lastURL, "platform=anthropic")
	require.NotContains(t, doer.lastURL, "platform=kiro")
}

func TestReconciler_SurfaceC_NeverWritesZeroOnFailure(t *testing.T) {
	stub := Account{
		ID:          31,
		Name:        "prod-mirror-us1",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 8,
		Credentials: map[string]any{
			"base_url":              "https://api-us1.tokenkey.dev",
			"api_key":               "sk-edge-key",
			"pool_mode":             true,
			"pool_mode_retry_count": 3,
		},
	}

	cases := []struct {
		name string
		doer *fakeDoer
	}{
		{"5xx", &fakeDoer{status: 503, body: "upstream down"}},
		{"transport error", &fakeDoer{err: io.ErrUnexpectedEOF}},
		{"capacity below one", &fakeDoer{status: 200, body: `{"data":{"total_concurrency":0}}`}},
		{"garbage body", &fakeDoer{status: 200, body: "not json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc := &reconcilerAccountStub{accounts: []Account{stub}, sum: 8}
			usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 8}}
			r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(true, false))
			r.http = tc.doer

			r.runOnce(context.Background())

			for _, call := range acc.bulkCalls {
				require.Nil(t, call.updates.Concurrency, "surface-C must NEVER write concurrency on a failed edge read (never 0)")
			}
		})
	}
}

func TestReconciler_TierDrift_ReportOnly_NoWrite(t *testing.T) {
	// l4 baseline is concurrency=10; this account drifted to 2 but is an OAUTH
	// account (not a stub), so the reconciler must only report, never write.
	drifted := Account{
		ID:          41,
		Name:        "edge-oauth-l4",
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
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(true, false))

	r.runOnce(context.Background())

	require.Empty(t, acc.bulkCalls, "tier drift must be report-only — never rewrite the account")
}

func TestReconciler_BalanceFloor_ResetsWhenBelowThreshold(t *testing.T) {
	acc := &reconcilerAccountStub{sum: 0}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 0, Balance: 12.5}}
	bal := &reconcilerBalanceStub{}
	r := newTestReconciler(acc, usr, bal, mirrorEnabledCfg(false, true))

	r.runOnce(context.Background())

	require.Equal(t, []float64{anthropicEdgeBalanceFloorDefault}, bal.setCalls,
		"balance below floor threshold must be reset to the default")
}

func TestReconciler_BalanceFloor_NoResetWhenHealthy(t *testing.T) {
	acc := &reconcilerAccountStub{sum: 0}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 0, Balance: 500}}
	bal := &reconcilerBalanceStub{}
	r := newTestReconciler(acc, usr, bal, mirrorEnabledCfg(false, true))

	r.runOnce(context.Background())

	require.Empty(t, bal.setCalls, "healthy balance must not be reset")
}

func TestReconciler_BalanceFloor_DisabledByConfig(t *testing.T) {
	acc := &reconcilerAccountStub{sum: 0}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 0, Balance: 1}}
	bal := &reconcilerBalanceStub{}
	r := newTestReconciler(acc, usr, bal, mirrorEnabledCfg(false, false))

	r.runOnce(context.Background())

	require.Empty(t, bal.setCalls, "balance floor must be a no-op when the toggle is off")
}

// --- kiro priority baseline self-heal ---------------------------------------

func TestReconciler_KiroPriorityBaseline_ValueSync(t *testing.T) {
	kiroAcct := Account{ID: 70, Name: "kiro-a", Platform: PlatformKiro, Type: AccountTypeOAuth, Priority: 3}
	acc := &reconcilerAccountStub{byPlatform: map[string][]Account{PlatformKiro: {kiroAcct}}}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 0}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(false, false))

	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1, "kiro account with priority!=10 must be value-synced")
	require.Equal(t, []int64{70}, acc.bulkCalls[0].ids)
	require.NotNil(t, acc.bulkCalls[0].updates.Priority)
	require.Equal(t, 10, *acc.bulkCalls[0].updates.Priority, "kiro priority must be hard-enforced to baseline 10")
	require.Nil(t, acc.bulkCalls[0].updates.Concurrency, "priority sync must not touch concurrency")
}

func TestReconciler_KiroPriorityBaseline_AlreadyAligned_NoWrite(t *testing.T) {
	kiroAcct := Account{ID: 71, Name: "kiro-b", Platform: PlatformKiro, Type: AccountTypeOAuth, Priority: 10}
	acc := &reconcilerAccountStub{byPlatform: map[string][]Account{PlatformKiro: {kiroAcct}}}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 0}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(false, false))

	r.runOnce(context.Background())

	require.Empty(t, acc.bulkCalls, "kiro account already at baseline 10 must not be written")
}

func TestReconciler_KiroPriorityBaseline_NonKiroUntouched(t *testing.T) {
	anth := Account{ID: 72, Name: "anth", Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 5}
	acc := &reconcilerAccountStub{byPlatform: map[string][]Account{
		PlatformAnthropic: {anth},
		// kiro absent → empty list → no kiro writes
	}}
	usr := &reconcilerUserStub{user: &User{ID: 1, Concurrency: 0}}
	r := newTestReconciler(acc, usr, &reconcilerBalanceStub{}, mirrorEnabledCfg(false, false))

	r.runOnce(context.Background())

	for _, c := range acc.bulkCalls {
		require.Nil(t, c.updates.Priority, "non-kiro accounts must never receive a priority value-sync")
	}
}
