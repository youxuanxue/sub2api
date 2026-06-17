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

// The pool-exhausted check is enabled per-platform; platforms outside the set
// (kiro/newapi/grok/antigravity) must not even query the repo.
func TestTkCheckPlatformPoolExhausted_DisabledPlatformIsNoop(t *testing.T) {
	repo := &poolExhaustedRepoStub{}
	notifier := &poolExhaustedNotifierStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAccountIncidentNotifier(notifier)

	service.tkCheckPlatformPoolExhausted(&Account{ID: 1, Platform: PlatformKiro}, time.Now(), "429")
	service.tkCheckPlatformPoolExhausted(nil, time.Now(), "429")

	// The armed path is async (delay + goroutine), so a disabled-platform
	// no-op is observable as: no goroutine ever queries the repo. Give any
	// stray goroutine a beat to surface before asserting.
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, repo.calls)
	require.Empty(t, notifier.pools)
}

// The 95%-pool incident (2026-06-17) extended the immediate P0 from
// anthropic-only to the gpt/google pools: openai + gemini must now be armed,
// while non-pool platforms stay out.
func TestTkPoolExhaustedEnabled_PlatformSet(t *testing.T) {
	for _, p := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini} {
		require.Truef(t, tkPoolExhaustedEnabled(p), "platform %q must be armed for pool-exhausted P0", p)
	}
	for _, p := range []string{PlatformKiro, PlatformNewAPI, PlatformGrok, PlatformAntigravity, ""} {
		require.Falsef(t, tkPoolExhaustedEnabled(p), "platform %q must not be armed", p)
	}
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
