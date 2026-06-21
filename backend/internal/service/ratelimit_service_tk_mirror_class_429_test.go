//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// newMirrorClass429Service wires a fresh RateLimitService with the given repo
// stub and a saturation counter whose IncrementSaturation returns a monotonic
// 1,2,3,… so the Nth HandleUpstreamError call sees count==N. This mirrors the
// prod MIRROR-account path (Anthropic apikey) where each forwarded edge 429
// re-increments the saturation counter.
func newMirrorClass429Service(repo *rateLimitAccountRepoStub) (*RateLimitService, *fakeSaturationCounterRL) {
	sat := &fakeSaturationCounterRL{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAnthropicSaturationCounter(sat)
	return svc, sat
}

func headerlessEmptyPoolBody() []byte {
	return []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
}

// 1) Sustained sonnet header-less 429 on a prod MIRROR account writes EXACTLY a
// class-scoped cooldown (anthropic:class:sonnet), reason
// anthropic_unified_window_exceeded, resetAt within [now, now+90s] — and NEVER
// an account-level SetRateLimited or the 3/3 ladder.
func TestMirrorClass429_SustainedSonnet_WritesClassScopedCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newMirrorClass429Service(repo)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := headerlessEmptyPoolBody()
	before := time.Now()
	// Drive past the sustained threshold (4-in-window): 5 hits.
	for i := 0; i < 5; i++ {
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, "claude-sonnet-4-5"))
	}
	after := time.Now()

	require.NotEmpty(t, repo.modelRateLimitCalls, "sustained sonnet 429 must write a class cooldown")
	// First write lands on the threshold-crossing hit (count==4); subsequent hits
	// are suppressed by the rewrite guard while remaining stays high.
	first := repo.modelRateLimitCalls[0]
	require.Equal(t, int64(54), first.accountID)
	require.Equal(t, "anthropic:class:sonnet", first.scope)
	require.Equal(t, "anthropic_unified_window_exceeded", first.reason)
	require.False(t, first.resetAt.Before(before), "resetAt must be >= now")
	require.False(t, first.resetAt.After(after.Add(time.Duration(tkAnthropicMirrorClassCooldownSeconds)*time.Second)),
		"resetAt must be within the bounded ~90s floor")

	require.Equal(t, 0, repo.setRateLimitedCalls, "must NEVER write account-level SetRateLimited")
	require.Equal(t, 0, repo.tempCalls, "must NEVER advance the ladder / temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
}

// 2) A single blip (count below the sustained threshold) writes NO class cooldown.
func TestMirrorClass429_BelowThreshold_NoCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newMirrorClass429Service(repo)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := headerlessEmptyPoolBody()
	// Only 3 hits → count never reaches the threshold (4).
	for i := 0; i < 3; i++ {
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, "claude-sonnet-4-5"))
	}
	require.Empty(t, repo.modelRateLimitCalls, "a transient blip must not cool the class")
	require.Equal(t, 0, repo.setRateLimitedCalls)
	require.Equal(t, 0, repo.tempCalls)
}

// 3) Unknown / absent class (empty model name) → no write even when sustained.
func TestMirrorClass429_UnknownClass_NoWrite(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newMirrorClass429Service(repo)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := headerlessEmptyPoolBody()
	for i := 0; i < 5; i++ {
		// No requestedModel arg → tkFirstRequestedModel == "" → unknown class.
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body))
	}
	require.Empty(t, repo.modelRateLimitCalls, "unknown class must never be guessed into a cooldown")
}

// 4) Amplifier-safety: cooling sonnet on the mirror MUST leave opus schedulable.
func TestMirrorClass429_Opus_OnlyCoolsOpus_SonnetSiblingStaysSchedulable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newMirrorClass429Service(repo)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := headerlessEmptyPoolBody()
	for i := 0; i < 5; i++ {
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, "claude-opus-4-8[1m]"))
	}
	require.NotEmpty(t, repo.modelRateLimitCalls)
	require.Equal(t, "anthropic:class:opus", repo.modelRateLimitCalls[0].scope)

	// Apply the written cooldown to a fresh account snapshot and assert sonnet is
	// NOT considered model-rate-limited (sibling class unaffected).
	cooled := accountWithClassCooldown(54, "anthropic:class:opus", repo.modelRateLimitCalls[0].resetAt)
	require.True(t, cooled.tkAnthropicModelClassRateLimitActive("claude-opus-4-8[1m]"),
		"opus must be cooled")
	require.False(t, cooled.tkAnthropicModelClassRateLimitActive("claude-sonnet-4-5"),
		"sonnet sibling must stay schedulable (amplifier-safety)")
}

// 5) Outbox-churn guard: a class already actively cooled with material remaining
// is NOT rewritten on the next sustained hit.
func TestMirrorClass429_AlreadyCooled_NoRewrite(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newMirrorClass429Service(repo)
	// Pre-seed an active sonnet cooldown with plenty of remaining (> rewrite floor).
	account := accountWithClassCooldown(54, "anthropic:class:sonnet", time.Now().Add(80*time.Second))
	account.Type = AccountTypeAPIKey

	body := headerlessEmptyPoolBody()
	for i := 0; i < 5; i++ {
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, "claude-sonnet-4-5"))
	}
	require.Empty(t, repo.modelRateLimitCalls,
		"an already-actively-cooled class must not be rewritten (outbox-churn guard)")
}

// 6) The non_authoritative_429 branch (cc-us7 header-less envelope from the
// ground-truth incident) also writes the class cooldown when sustained. This
// body matches NO needle but IS header-less, so it lands on the
// tkIsAnthropicNonAuthoritative429 skip branch.
func TestMirrorClass429_NonAuthoritative429Branch_WritesCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newMirrorClass429Service(repo)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	// Header-less 429 whose body is NOT a TK capacity needle ("no available
	// accounts" / "all available accounts exhausted") — the cc-us7 relayed
	// "Upstream rate limit exceeded" envelope.
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Upstream rate limit exceeded, please retry later"}}`)
	for i := 0; i < 5; i++ {
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, "claude-sonnet-4-5"))
	}
	require.NotEmpty(t, repo.modelRateLimitCalls,
		"header-less non-authoritative 429 must class-cool when sustained")
	require.Equal(t, "anthropic:class:sonnet", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, 0, repo.setRateLimitedCalls)
	require.Equal(t, 0, repo.tempCalls)
}

// 7) The all_available_accounts_exhausted branch (failover-exhausted sibling
// capacity envelope) also writes the class cooldown when sustained — covering
// the second of the three new injection points. NOTE: this injection point lives
// in HandleUpstreamError's `case 429:` block, so it fires for the
// failover-exhausted body relayed with a 429 status (e.g. empty-pool fast-fail).
// A genuine HTTP 502 routes through the `default:` → legacy
// handleAnthropicUpstreamError path, which is intentionally NOT wired (it lacks
// requestedModel) and is documented as out-of-scope — see TestMirrorClass429_
// Genuine502_LegacyPath_NoMirrorClassCooldown for that boundary.
func TestMirrorClass429_FailoverExhaustedBody429_WritesCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newMirrorClass429Service(repo)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	// 429 whose body is TokenKey's own failover-exhausted capacity phrase.
	body := []byte(`{"type":"error","error":{"type":"server_error","message":"all available accounts exhausted"}}`)
	for i := 0; i < 5; i++ {
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, "claude-sonnet-4-5"))
	}
	require.NotEmpty(t, repo.modelRateLimitCalls,
		"sustained failover-exhausted 429 must class-cool the mirror account")
	require.Equal(t, "anthropic:class:sonnet", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, "anthropic_unified_window_exceeded", repo.modelRateLimitCalls[0].reason)
	require.Equal(t, 0, repo.setRateLimitedCalls)
	require.Equal(t, 0, repo.tempCalls)
}

// 8) Boundary: a genuine HTTP 502 for an Anthropic mirror account routes through
// the legacy handleAnthropicUpstreamError path, which is intentionally NOT wired
// for the mirror per-class cooldown (it lacks requestedModel). Asserts no
// mirror-class cooldown is written there — documenting the deliberate scope
// boundary so a future reader does not assume 502 is covered.
func TestMirrorClass429_Genuine502_LegacyPath_NoMirrorClassCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newMirrorClass429Service(repo)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := []byte(`{"type":"error","error":{"type":"server_error","message":"all available accounts exhausted"}}`)
	for i := 0; i < 5; i++ {
		// 502 → default: → legacy handleAnthropicUpstreamError (no requestedModel).
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusBadGateway, http.Header{}, body, "claude-sonnet-4-5"))
	}
	require.Empty(t, repo.modelRateLimitCalls,
		"the legacy 502 path is intentionally NOT wired for the mirror per-class cooldown")
	require.Equal(t, 0, repo.setRateLimitedCalls)
	require.Equal(t, 0, repo.tempCalls)
}

// accountWithClassCooldown builds an Account whose Extra carries an active
// model-class cooldown at the given scope/resetAt, matching the JSON shape
// modelRateLimitResetAt reads.
func accountWithClassCooldown(id int64, scope string, resetAt time.Time) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				scope: map[string]any{
					"rate_limit_reset_at": resetAt.Format(time.RFC3339),
				},
			},
		},
	}
}
