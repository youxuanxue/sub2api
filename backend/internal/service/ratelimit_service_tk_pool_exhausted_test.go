//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type poolExhaustedRepoStub struct {
	mockAccountRepoForGemini
	schedulable []Account
	queryErr    error
	calls       int
}

func (r *poolExhaustedRepoStub) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
	r.calls++
	return r.schedulable, r.queryErr
}

type poolExhaustedNotifierStub struct {
	incidents int
	pools     []string
}

func (n *poolExhaustedNotifierStub) NotifyAccountIncident(account *Account, until time.Time, reason string, kind AccountIncidentKind, detail ...string) {
	n.incidents++
}
func (n *poolExhaustedNotifierStub) NotifyAccountRecovered(accountID int64) {}
func (n *poolExhaustedNotifierStub) NotifyPlatformPoolExhausted(platform string, trigger *Account, until time.Time, reason string) {
	n.pools = append(n.pools, platform)
}

// Incident 2026-06-11: the moment the LAST schedulable anthropic account got
// cooled, the pool went dark for ~10 minutes with no alert — the P0 card must
// fire exactly on the 0-schedulable transition.
func TestTkPlatformPoolExhaustedCheck_FiresOnEmptyPool(t *testing.T) {
	repo := &poolExhaustedRepoStub{schedulable: nil}
	notifier := &poolExhaustedNotifierStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountIncidentNotifier(notifier)
	trigger := &Account{ID: 7, Name: "cc-us7", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	service.tkPlatformPoolExhaustedCheck(context.Background(), PlatformAnthropic, trigger, time.Now().Add(10*time.Minute), "temp_unschedulable")

	require.Equal(t, []string{PlatformAnthropic}, notifier.pools)
}

// Transient drain (edge-us7 2026-06-18): the edge's only anthropic account hits a
// single upstream 529, gets a ~30s tier-0 overload cooldown, and the pool momentarily
// reads 0 schedulable — but it self-heals in seconds (prod→edge mirror relay fails
// over to sibling edges meanwhile). A short cooldown (<= 60s) means tier-0 transient,
// not a sustained outage, so it must NOT page P0 — it downgrades to a WARN.
func TestTkPlatformPoolExhaustedCheck_TransientCooldownDoesNotPage(t *testing.T) {
	repo := &poolExhaustedRepoStub{schedulable: nil}
	notifier := &poolExhaustedNotifierStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountIncidentNotifier(notifier)
	trigger := &Account{ID: 1, Name: "edge-ls-oh-4-d", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	service.tkPlatformPoolExhaustedCheck(context.Background(), PlatformAnthropic, trigger, time.Now().Add(30*time.Second), "529")

	require.Empty(t, notifier.pools, "transient ~30s tier-0 cooldown must not page P0 (self-heals)")
}

// Continuous 529/503 escalates via the 3/3 ladder into tier-1 (2m) / tier-2 (10m). A
// long remaining cooldown (> 60s) means the account is in a sustained outage — that
// MUST still page immediately, on a single-account edge as much as a multi-account
// pool (the 2026-06-11 seven-stub collapse was 10m cooldowns).
func TestTkPlatformPoolExhaustedCheck_SustainedCooldownPages(t *testing.T) {
	repo := &poolExhaustedRepoStub{schedulable: nil}
	notifier := &poolExhaustedNotifierStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountIncidentNotifier(notifier)
	trigger := &Account{ID: 1, Name: "edge-ls-oh-4-d", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	service.tkPlatformPoolExhaustedCheck(context.Background(), PlatformAnthropic, trigger, time.Now().Add(2*time.Minute), "529")

	require.Equal(t, []string{PlatformAnthropic}, notifier.pools, "sustained (tier-1+) cooldown must page P0 even for a single account")
}

// Conservative fail-open: a zero/unknown cooldown time cannot be proven transient, so
// keep the P0 — better a spurious page than a missed real collapse.
func TestTkPlatformPoolExhaustedCheck_ZeroUntilFailsOpenToPage(t *testing.T) {
	repo := &poolExhaustedRepoStub{schedulable: nil}
	notifier := &poolExhaustedNotifierStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountIncidentNotifier(notifier)
	trigger := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	service.tkPlatformPoolExhaustedCheck(context.Background(), PlatformAnthropic, trigger, time.Time{}, "529")

	require.Equal(t, []string{PlatformAnthropic}, notifier.pools, "zero/unknown until must fail open to P0, not silently suppress")
}

func TestTkPlatformPoolExhaustedCheck_QuietWhenPoolHasCapacity(t *testing.T) {
	repo := &poolExhaustedRepoStub{schedulable: []Account{{ID: 1, Platform: PlatformAnthropic}}}
	notifier := &poolExhaustedNotifierStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountIncidentNotifier(notifier)
	trigger := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	service.tkPlatformPoolExhaustedCheck(context.Background(), PlatformAnthropic, trigger, time.Now(), "429")

	require.Empty(t, notifier.pools, "pool with schedulable accounts must not alert")
}

func TestTkPlatformPoolExhaustedCheck_QuietOnQueryError(t *testing.T) {
	repo := &poolExhaustedRepoStub{queryErr: context.DeadlineExceeded}
	notifier := &poolExhaustedNotifierStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountIncidentNotifier(notifier)
	trigger := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	service.tkPlatformPoolExhaustedCheck(context.Background(), PlatformAnthropic, trigger, time.Now(), "429")

	require.Empty(t, notifier.pools, "query failure must fail quiet, not page")
}

// The pool-exhausted check is derived (any non-empty platform armed). Only a
// nil account / empty platform is a no-op — it must not even query the repo.
func TestTkCheckPlatformPoolExhausted_EmptyPlatformIsNoop(t *testing.T) {
	repo := &poolExhaustedRepoStub{}
	notifier := &poolExhaustedNotifierStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountIncidentNotifier(notifier)

	service.tkCheckPlatformPoolExhausted(&Account{ID: 1, Platform: ""}, time.Now(), "429")
	service.tkCheckPlatformPoolExhausted(nil, time.Now(), "429")

	// The armed path is async (delay + goroutine), so a no-op is observable as:
	// no goroutine ever queries the repo. Give any stray goroutine a beat to
	// surface before asserting.
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, repo.calls)
	require.Empty(t, notifier.pools)
}

// Derived from the scheduling partition (2026-06): every real platform is armed
// for the empty-pool P0 — newapi/kiro/grok/antigravity onboard automatically,
// no allowlist edit. Only an empty platform string stays out.
func TestTkPoolExhaustedEnabled_PlatformSet(t *testing.T) {
	for _, p := range []string{
		PlatformAnthropic, PlatformOpenAI, PlatformGemini,
		PlatformKiro, PlatformNewAPI, PlatformGrok, PlatformAntigravity,
	} {
		require.Truef(t, tkPoolExhaustedEnabled(p), "platform %q must be armed for pool-exhausted P0", p)
	}
	require.False(t, tkPoolExhaustedEnabled(""), "empty platform must not be armed")
	require.False(t, tkPoolExhaustedEnabled("   "), "blank platform must not be armed")
}

// The sync check body is platform-agnostic (gating lives in the async wrapper),
// so an empty openai/gemini pool fires the P0 just like anthropic does.
func TestTkPlatformPoolExhaustedCheck_FiresForOpenAIAndGemini(t *testing.T) {
	for _, platform := range []string{PlatformOpenAI, PlatformGemini} {
		repo := &poolExhaustedRepoStub{schedulable: nil}
		notifier := &poolExhaustedNotifierStub{}
		service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		service.SetAccountIncidentNotifier(notifier)
		trigger := &Account{ID: 16, Platform: platform, Type: AccountTypeAPIKey}

		service.tkPlatformPoolExhaustedCheck(context.Background(), platform, trigger, time.Now().Add(10*time.Minute), "429")

		require.Equalf(t, []string{platform}, notifier.pools, "empty %s pool must page P0", platform)
	}
}
