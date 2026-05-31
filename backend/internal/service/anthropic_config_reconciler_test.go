//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"

	"github.com/stretchr/testify/require"
)

// --- stubs -------------------------------------------------------------------

type reconcilerAccountStub struct {
	accounts  []Account
	sum       int64
	bulkCalls []bulkUpdateCall
	listErr   error
	sumErr    error
	bulkErr   error
}

type bulkUpdateCall struct {
	ids     []int64
	updates AccountBulkUpdate
}

func (s *reconcilerAccountStub) ListByPlatform(ctx context.Context, platform string) ([]Account, error) {
	if s.listErr != nil {
		return nil, s.listErr
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
	r := NewAnthropicConfigReconciler(acc, usr, bal, nil, cfg, nil)
	return r
}

// stubTierConcurrency is a minimal reconcilerTierResolver for Step T tests.
type stubTierConcurrency struct{ conc map[int64]int }

func (s *stubTierConcurrency) ResolveConcurrency(tierID int64) (int, bool) {
	v, ok := s.conc[tierID]
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
