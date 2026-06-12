//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// fakeBalanceDoer returns a canned DeepSeek /user/balance body per call, driven by
// a script so each runOnce pass can present a different balance.
type fakeBalanceDoer struct {
	mu     sync.Mutex
	bodies []string // consumed FIFO; last one repeats
	calls  int
}

func (d *fakeBalanceDoer) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	idx := d.calls
	if idx >= len(d.bodies) {
		idx = len(d.bodies) - 1
	}
	body := d.bodies[idx]
	d.calls++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

// fakeFeishuCfg yields a config with Feishu enabled (so runOnce proceeds) but no
// webhook URL — so the notifier's async send is a safe no-op (no network).
type fakeFeishuCfg struct {
	threshold float64
	enabled   bool
}

func (c fakeFeishuCfg) GetEmailNotificationConfig(_ context.Context) (*OpsEmailNotificationConfig, error) {
	return &OpsEmailNotificationConfig{
		Feishu: OpsFeishuAlertConfig{
			Enabled:                        c.enabled,
			UpstreamBalanceLowThresholdCNY: c.threshold,
		},
	}, nil
}

type fakeAccountStore struct{ accounts []Account }

func (s fakeAccountStore) ListByPlatform(_ context.Context, _ string) ([]Account, error) {
	return s.accounts, nil
}

// The low-balance alert method is on the concrete *TKAccountIncidentNotifier, so
// these tests assert the deterministic re-arm invariant on the sentinel's `armed`
// map (the alert is fired iff armed flips false→true), and separately unit-test
// the card text. The notifier itself sends nothing because its config carries no
// webhook URL (send() short-circuits), so there is no network in these tests.

func timeFixed() time.Time { return time.Date(2026, 6, 12, 8, 0, 0, 0, time.UTC) }

func dsBody(balance string, available bool) string {
	avail := "true"
	if !available {
		avail = "false"
	}
	return `{"is_available":` + avail + `,"balance_infos":[{"currency":"CNY","total_balance":"` + balance + `"}]}`
}

func newTestSentinel(doer *fakeBalanceDoer, store fakeAccountStore, threshold float64) *UpstreamBalanceSentinel {
	notifier := newTKAccountIncidentNotifier(fakeFeishuCfg{threshold: threshold, enabled: true}, "test")
	return NewUpstreamBalanceSentinel(
		store,
		doer,
		notifier,
		fakeFeishuCfg{threshold: threshold, enabled: true},
		nil, // recharge resolver: nil → no link
		nil, // heartbeat: nil → skipped
		nil, // redis: nil → lockless
	)
}

func dsAccount(id int64) Account {
	return Account{
		ID:          id,
		Name:        "ds-test",
		Platform:    PlatformNewAPI,
		Status:      StatusActive,
		ChannelType: newapiconstant.ChannelTypeDeepSeek,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.deepseek.com"},
	}
}

// TestSentinelReArmDedup drives the core invariant: alert on first crossing into
// low (armed=true), stay armed while still low, re-arm (armed cleared) on recovery,
// and re-arm again on the next dip.
func TestSentinelReArmDedup(t *testing.T) {
	const threshold = 50.0
	acc := dsAccount(39)
	// Script: low(10) → low(5) → recovered(298) → low(8)
	doer := &fakeBalanceDoer{bodies: []string{
		dsBody("10.00", true),  // pass 1: low → arm
		dsBody("5.00", true),   // pass 2: still low → stays armed, no re-alert
		dsBody("298.00", true), // pass 3: recovered → disarm
		dsBody("8.00", true),   // pass 4: low again → re-arm
	}}
	s := newTestSentinel(doer, fakeAccountStore{accounts: []Account{acc}}, threshold)
	ctx := context.Background()

	s.runOnce(ctx)
	if !s.armed[39] {
		t.Fatalf("pass1: expected account armed after first low crossing")
	}
	s.runOnce(ctx)
	if !s.armed[39] {
		t.Fatalf("pass2: expected account to stay armed while still low")
	}
	s.runOnce(ctx)
	if s.armed[39] {
		t.Fatalf("pass3: expected account disarmed after recovery above threshold")
	}
	s.runOnce(ctx)
	if !s.armed[39] {
		t.Fatalf("pass4: expected account re-armed on the next dip below threshold")
	}
	if doer.calls != 4 {
		t.Errorf("expected 4 probe calls, got %d", doer.calls)
	}
}

// TestSentinelIsAvailableFalseArms verifies the upstream "is_available=false" hard
// signal alone arms the account even if the parsed balance is above threshold.
func TestSentinelIsAvailableFalseArms(t *testing.T) {
	const threshold = 50.0
	acc := dsAccount(39)
	// balance string above threshold, but is_available=false → still low.
	doer := &fakeBalanceDoer{bodies: []string{dsBody("999.00", false)}}
	s := newTestSentinel(doer, fakeAccountStore{accounts: []Account{acc}}, threshold)
	s.runOnce(context.Background())
	if !s.armed[39] {
		t.Fatalf("expected account armed when upstream reports is_available=false")
	}
}

// TestSentinelFeatureOffSkips verifies that with Feishu disabled, runOnce makes no
// upstream probe call at all.
func TestSentinelFeatureOffSkips(t *testing.T) {
	doer := &fakeBalanceDoer{bodies: []string{dsBody("1.00", true)}}
	notifier := newTKAccountIncidentNotifier(fakeFeishuCfg{threshold: 50, enabled: false}, "test")
	s := NewUpstreamBalanceSentinel(
		fakeAccountStore{accounts: []Account{dsAccount(39)}},
		doer, notifier,
		fakeFeishuCfg{threshold: 50, enabled: false},
		nil, nil, nil,
	)
	s.runOnce(context.Background())
	if doer.calls != 0 {
		t.Errorf("expected 0 probe calls when feature off, got %d", doer.calls)
	}
	if s.armed[39] {
		t.Errorf("expected no arming when feature off")
	}
}

// TestSentinelNonDeepSeekSkipped verifies a non-probeable channel type is skipped.
func TestSentinelNonDeepSeekSkipped(t *testing.T) {
	acc := dsAccount(39)
	acc.ChannelType = 0 // not registered
	doer := &fakeBalanceDoer{bodies: []string{dsBody("1.00", true)}}
	s := newTestSentinel(doer, fakeAccountStore{accounts: []Account{acc}}, 50)
	s.runOnce(context.Background())
	if doer.calls != 0 {
		t.Errorf("expected 0 probe calls for non-DeepSeek channel, got %d", doer.calls)
	}
}

// TestBuildUpstreamBalanceLowText sanity-checks the card body renders the key
// fields without panicking on a minimal account.
func TestBuildUpstreamBalanceLowText(t *testing.T) {
	acc := dsAccount(39)
	body := buildUpstreamBalanceLowText("prod", &acc, 12.34, true, 50, "https://recharge.example", timeFixed())
	for _, want := range []string{"12.34", "50.00", "recharge.example", "ds-test", "newapi"} {
		if !strings.Contains(body, want) {
			t.Errorf("card body missing %q\n%s", want, body)
		}
	}
	// is_available=false adds the urgent line.
	body2 := buildUpstreamBalanceLowText("prod", &acc, 0, false, 50, "", timeFixed())
	if !strings.Contains(body2, "余额可能已归零") {
		t.Errorf("expected urgent availability note when is_available=false\n%s", body2)
	}
}

type fakeHeartbeat struct{ last *OpsUpsertJobHeartbeatInput }

func (h *fakeHeartbeat) UpsertJobHeartbeat(_ context.Context, in *OpsUpsertJobHeartbeatInput) error {
	h.last = in
	return nil
}

// TestSentinelHeartbeatCarriesBalance drives runOnce end-to-end and asserts the
// probed account's actual balance lands in the heartbeat last_result.
func TestSentinelHeartbeatCarriesBalance(t *testing.T) {
	hb := &fakeHeartbeat{}
	doer := &fakeBalanceDoer{bodies: []string{dsBody("3262.07", true)}}
	notifier := newTKAccountIncidentNotifier(fakeFeishuCfg{threshold: 50, enabled: true}, "test")
	s := NewUpstreamBalanceSentinel(
		fakeAccountStore{accounts: []Account{dsAccount(39)}},
		doer, notifier,
		fakeFeishuCfg{threshold: 50, enabled: true},
		nil, hb, nil,
	)
	s.runOnce(context.Background())
	if hb.last == nil || hb.last.LastResult == nil {
		t.Fatalf("expected a success heartbeat with last_result")
	}
	got := *hb.last.LastResult
	for _, want := range []string{"checked=1", "low=0", "probe_errors=0", "balances=[ds-test:3262.07]"} {
		if !strings.Contains(got, want) {
			t.Errorf("last_result missing %q: %s", want, got)
		}
	}
}

func TestFormatSentinelHeartbeatResult(t *testing.T) {
	if got := formatSentinelHeartbeatResult(0, 0, 0, nil); got != "checked=0 low=0 probe_errors=0" {
		t.Errorf("no-balance form wrong: %q", got)
	}
	if got := formatSentinelHeartbeatResult(2, 1, 0, []string{"ds-x:42.50", "y:100.00"}); got != "checked=2 low=1 probe_errors=0 balances=[ds-x:42.50,y:100.00]" {
		t.Errorf("balance form wrong: %q", got)
	}
	big := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		big = append(big, "acctname:1234.56")
	}
	if got := formatSentinelHeartbeatResult(500, 0, 0, big); len(got) > 2048 {
		t.Errorf("expected clamp to 2048, got %d", len(got))
	}
}

func TestSanitizeBalanceSampleName(t *testing.T) {
	cases := map[string]string{
		"ds-官":       "ds-官",
		"a,b:c[d]e":  "a_b_c_d_e",
		"  spaced  ": "spaced",
		"":           "?",
	}
	for in, want := range cases {
		if got := sanitizeBalanceSampleName(in); got != want {
			t.Errorf("sanitize(%q)=%q want %q", in, got, want)
		}
	}
}
