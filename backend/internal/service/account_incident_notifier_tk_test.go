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

	"github.com/stretchr/testify/require"
)

// --- fakes ---------------------------------------------------------------

type fakeIncidentConfigProvider struct {
	cfg *OpsEmailNotificationConfig
	err error
}

func (f *fakeIncidentConfigProvider) GetEmailNotificationConfig(_ context.Context) (*OpsEmailNotificationConfig, error) {
	return f.cfg, f.err
}

func enabledFeishuConfig() *OpsEmailNotificationConfig {
	return &OpsEmailNotificationConfig{
		Feishu: OpsFeishuAlertConfig{
			Enabled:                      true,
			WebhookURL:                   "https://open.feishu.cn/open-apis/bot/v2/hook/test",
			AccountIncidentDigestEnabled: true, // 显式开自愈摘要（opt-in）
			AccountIncidentDigestSeconds: 600,
		},
	}
}

// disabledDigestFeishuConfig keeps Feishu on but turns the self-healing
// temporary-cooldown digest OFF (opt-in): account_incident_digest_enabled=false.
// seconds stays > 0 to prove enable is gated on the bool, NOT on seconds.
func disabledDigestFeishuConfig() *OpsEmailNotificationConfig {
	cfg := enabledFeishuConfig()
	cfg.Feishu.AccountIncidentDigestEnabled = false
	return cfg
}

type blockingFeishuDoer struct {
	mu     sync.Mutex
	calls  int
	bodies []string
	block  chan struct{} // if non-nil, Do blocks on receive
	done   chan struct{} // if non-nil, Do signals after each call
	panics bool
}

func (d *blockingFeishuDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		d.mu.Lock()
		d.bodies = append(d.bodies, string(body))
		d.mu.Unlock()
	}
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	if d.block != nil {
		<-d.block
	}
	if d.panics {
		panic("boom from doer")
	}
	if d.done != nil {
		d.done <- struct{}{}
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"code":0}`)), Header: http.Header{}}, nil
}

func (d *blockingFeishuDoer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *blockingFeishuDoer) lastBody() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.bodies) == 0 {
		return ""
	}
	return d.bodies[len(d.bodies)-1]
}

func newTestNotifier(provider opsFeishuConfigProvider, doer opsFeishuHTTPDoer, fixedNow time.Time) *TKAccountIncidentNotifier {
	n := newTKAccountIncidentNotifier(provider, "prod")
	n.httpClient = doer
	n.now = func() time.Time { return fixedNow }
	return n
}

func testAccount(id int64, name, platform string) *Account {
	return &Account{ID: id, Name: name, Platform: platform, Groups: []*Group{{ID: 7, Name: "group15"}}}
}

// --- tests ---------------------------------------------------------------

func TestClassifyIncident(t *testing.T) {
	t.Parallel()
	cases := []struct {
		reason    string
		until     time.Time
		kind      AccountIncidentKind
		wantKind  AccountIncidentKind
		wantClass string
	}{
		{"auth_error", time.Time{}, IncidentKindUnknown, IncidentKindPermanentDisable, "auth"},
		{"custom_error_code", time.Time{}, IncidentKindUnknown, IncidentKindPermanentDisable, "custom_code"},
		{"stream_timeout_error", time.Time{}, IncidentKindUnknown, IncidentKindPermanentDisable, "stream_timeout"},
		{"oauth_401", time.Now(), IncidentKindUnknown, IncidentKindTemporaryCooldown, "oauth401"},
		{"429", time.Now(), IncidentKindUnknown, IncidentKindTemporaryCooldown, "429"},
		{"429_fallback", time.Now(), IncidentKindUnknown, IncidentKindTemporaryCooldown, "429"},
		{"529", time.Now(), IncidentKindUnknown, IncidentKindTemporaryCooldown, "529"},
		{"openai_403_temp", time.Now(), IncidentKindUnknown, IncidentKindTemporaryCooldown, "403"},
		{"temp_unschedulable", time.Now(), IncidentKindUnknown, IncidentKindTemporaryCooldown, "temp"},
		{"stream_timeout_temp_unschedulable", time.Now(), IncidentKindUnknown, IncidentKindTemporaryCooldown, "temp"},
		{"kiro_quota_limit", time.Time{}, IncidentKindUnknown, IncidentKindPermanentDisable, "kiro_quota_limit"},
		// unknown reason → fall back to until/kind
		{"mystery", time.Time{}, IncidentKindUnknown, IncidentKindPermanentDisable, "other"},
		{"mystery", time.Now(), IncidentKindUnknown, IncidentKindTemporaryCooldown, "other"},
		{"mystery", time.Time{}, IncidentKindTemporaryCooldown, IncidentKindTemporaryCooldown, "other"},
	}
	for _, c := range cases {
		got := classifyIncident(c.reason, c.until, c.kind)
		require.True(t, got.alert, "reason=%s should alert", c.reason)
		require.Equal(t, c.wantKind, got.kind, "reason=%s kind", c.reason)
		require.Equal(t, c.wantClass, got.reasonClass, "reason=%s class", c.reason)
		require.NotEmpty(t, got.kindZh)
		require.NotEmpty(t, got.advice)
	}
}

func TestFormatAlertTime(t *testing.T) {
	t.Parallel()
	utc := time.Date(2026, 6, 5, 10, 31, 34, 0, time.UTC)
	got := formatAlertTime(utc)
	require.Contains(t, got, "2026-06-05T10:31:34Z")
	require.Contains(t, got, "北京时间 18:31:34")
}

func TestSiteFromFrontendURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://api.tokenkey.dev":          "prod",
		"https://api-us1.tokenkey.dev":      "edge-us1",
		"https://api-uk1.tokenkey.dev/":     "edge-uk1",
		"https://api-us2.tokenkey.dev:8443": "edge-us2",
		"":                                  "unknown",
		"not a url":                         "unknown",
	}
	for in, want := range cases {
		require.Equal(t, want, siteFromFrontendURL(in), "input=%q", in)
	}
}

func TestIsEdgeFrontendURL(t *testing.T) {
	t.Parallel()
	require.True(t, IsEdgeFrontendURL("https://api-us3.tokenkey.dev"))
	require.False(t, IsEdgeFrontendURL("https://api.tokenkey.dev"))
	require.False(t, IsEdgeFrontendURL(""))
	require.False(t, IsEdgeFrontendURL("https://gateway.example.com"))
}

func TestNotifyPermanentSendsImmediately(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 1)}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))

	n.NotifyAccountIncident(testAccount(42, "cc-main", "anthropic"), time.Time{}, "auth_error", IncidentKindUnknown)

	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("permanent incident did not send within 2s")
	}
	require.Equal(t, 1, doer.callCount())
	body := doer.lastBody()
	require.Contains(t, body, "cc-main")
	require.Contains(t, body, "anthropic")
	require.Contains(t, body, "group15")
}

func TestNotifyTemporaryBuffersUntilFlush(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 1)}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))

	// Two different accounts hit 429 — must NOT send immediately.
	n.NotifyAccountIncident(testAccount(1, "acc1", "openai"), time.Now().Add(time.Minute), "429", IncidentKindUnknown)
	n.NotifyAccountIncident(testAccount(2, "acc2", "openai"), time.Now().Add(time.Minute), "429", IncidentKindUnknown)
	require.Equal(t, 0, doer.callCount(), "temporary incidents must not send immediately")

	// buffer has one reasonClass with two accounts
	n.mu.Lock()
	require.Len(t, n.digest, 1)
	require.Equal(t, 2, n.digest["429"].count)
	require.Len(t, n.digest["429"].accountIDs, 2)
	n.mu.Unlock()

	n.flushDigest()
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("digest flush did not send within 2s")
	}
	require.Equal(t, 1, doer.callCount())
	require.Contains(t, doer.lastBody(), "限流冷却")

	// buffer cleared after flush
	n.mu.Lock()
	require.Empty(t, n.digest)
	n.mu.Unlock()
}

// Self-healing temporary-cooldown digest is opt-in (default off). When
// account_incident_digest_seconds<=0, a 429/529 temp cooldown must NOT buffer,
// NOT register an active-recovery ledger entry, and NOT send — while the
// pool-level全不可调度 P0 and permanent-failure P0 stay on their always-fire paths.
func TestTemporaryDigestDisabled_SilencesSelfHealingButKeepsP0(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 4)}
	fixedNow := time.Date(2026, 6, 11, 16, 50, 0, 0, time.UTC)
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: disabledDigestFeishuConfig()}, doer, fixedNow)

	// Temporary cooldown (529) is fully silenced.
	n.NotifyAccountIncident(testAccount(1, "cc-us3", "anthropic"), fixedNow.Add(30*time.Second), "529", IncidentKindUnknown)
	n.mu.Lock()
	require.Empty(t, n.digest, "disabled digest must not buffer temporary cooldowns")
	require.Empty(t, n.active, "disabled digest must not track temporary cooldowns for recovery")
	n.mu.Unlock()
	n.flushDigest()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 0, doer.callCount(), "no temporary digest send when disabled")

	// Pool-level P0 still fires.
	n.NotifyPlatformPoolExhausted(PlatformAnthropic, testAccount(1, "cc-us3", "anthropic"), fixedNow.Add(30*time.Second), "529")
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pool-exhausted P0 must still fire when digest disabled")
	}
	require.Equal(t, 1, doer.callCount())
	require.Contains(t, doer.lastBody(), "平台池全不可调度")

	// Permanent failure P0 still fires immediately.
	n.NotifyAccountIncident(testAccount(2, "cc-us6", "anthropic"), time.Time{}, "auth_error", IncidentKindUnknown)
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("permanent P0 must still fire when digest disabled")
	}
	require.Equal(t, 2, doer.callCount())
}

func TestModelClassCooldownDetailSubdividesDigest(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 1)}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))

	// Same reasonClass (429_model_class) but two different upstream dimensions →
	// must NOT collapse into one line; the operator needs to see opus vs sonnet.
	n.NotifyAccountIncident(testAccount(1, "edge-ls-oh-3-d", "anthropic"), time.Now().Add(time.Hour), "429_model_class", IncidentKindUnknown, "opus·5h 窗口")
	n.NotifyAccountIncident(testAccount(2, "cc-main", "anthropic"), time.Now().Add(time.Hour), "429_model_class", IncidentKindUnknown, "sonnet·7d 窗口")

	n.mu.Lock()
	require.Len(t, n.digest, 2, "two distinct details must produce two digest entries")
	n.mu.Unlock()

	n.flushDigest()
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("digest flush did not send within 2s")
	}
	body := doer.lastBody()
	require.Contains(t, body, "模型类限流冷却")
	require.Contains(t, body, "opus·5h 窗口")
	require.Contains(t, body, "sonnet·7d 窗口")
	require.Contains(t, body, "edge-ls-oh-3-d")
}

func TestAccount429WindowDetailRendered(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 1)}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))

	n.NotifyAccountIncident(testAccount(55, "cc-us6", "anthropic"), time.Now().Add(time.Hour), "429", IncidentKindUnknown, "5h 窗口")

	n.flushDigest()
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("digest flush did not send within 2s")
	}
	body := doer.lastBody()
	require.Contains(t, body, "限流冷却（429）｜5h 窗口")
	require.Contains(t, body, "cc-us6")
	// Footer must correct the rpm/concurrency/session misconception (Q2).
	require.Contains(t, body, "非 TK 内部 rpm/并发/会话 配额")
	require.Contains(t, body, "no available accounts")
}

func TestEmptyDetailKeepsLegacyDigestKey(t *testing.T) {
	doer := &blockingFeishuDoer{}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))

	// No detail (e.g. OpenAI / fallback 429) must still aggregate under the bare
	// reasonClass key, preserving historical behaviour.
	n.NotifyAccountIncident(testAccount(9, "openai-1", "openai"), time.Now().Add(time.Minute), "429", IncidentKindUnknown)
	n.mu.Lock()
	require.NotNil(t, n.digest["429"], "empty-detail 429 must key under bare reasonClass")
	require.Equal(t, "", n.digest["429"].detail)
	n.mu.Unlock()
}

func TestFlushEmptyBufferDoesNotSend(t *testing.T) {
	doer := &blockingFeishuDoer{}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))
	n.flushDigest()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 0, doer.callCount())
}

func TestPermanentDedupeWithinWindow(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 2)}
	now := time.Unix(1700000000, 0)
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, now)

	acc := testAccount(42, "cc-main", "anthropic")
	n.NotifyAccountIncident(acc, time.Time{}, "auth_error", IncidentKindUnknown)
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("first permanent incident did not send")
	}
	// second, same (site,account,reasonClass) within 1h window (now unchanged) → suppressed
	n.NotifyAccountIncident(acc, time.Time{}, "auth_error", IncidentKindUnknown)
	select {
	case <-doer.done:
		t.Fatal("duplicate permanent incident should have been suppressed")
	case <-time.After(150 * time.Millisecond):
	}
	require.Equal(t, 1, doer.callCount())
}

func TestNotifyDisabledDoesNotSend(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 1)}
	disabled := &OpsEmailNotificationConfig{Feishu: OpsFeishuAlertConfig{Enabled: false}}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: disabled}, doer, time.Unix(1700000000, 0))

	n.NotifyAccountIncident(testAccount(42, "cc-main", "anthropic"), time.Time{}, "auth_error", IncidentKindUnknown)
	select {
	case <-doer.done:
		t.Fatal("must not send when feishu disabled")
	case <-time.After(150 * time.Millisecond):
	}
	require.Equal(t, 0, doer.callCount())
}

func TestNotifyDoesNotBlockOnSlowHTTP(t *testing.T) {
	block := make(chan struct{})
	doer := &blockingFeishuDoer{block: block}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))

	start := time.Now()
	n.NotifyAccountIncident(testAccount(42, "cc-main", "anthropic"), time.Time{}, "auth_error", IncidentKindUnknown)
	elapsed := time.Since(start)
	require.Less(t, elapsed, 50*time.Millisecond, "hook point must return immediately even if HTTP is stuck")

	close(block) // release the goroutine for clean teardown
}

func TestSendPanicRecovered(t *testing.T) {
	doer := &blockingFeishuDoer{panics: true}
	n := newTestNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Unix(1700000000, 0))
	// sendNow runs synchronously here; a panic in the doer must be recovered, not crash the test.
	require.NotPanics(t, func() {
		n.sendNow("title", "red", "body", "test")
	})
}
