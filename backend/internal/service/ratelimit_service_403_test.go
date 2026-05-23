//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type runtimeBlockRecorder struct {
	accounts   []*Account
	until      []time.Time
	reasons    []string
	clearedIDs []int64
}

func (r *runtimeBlockRecorder) BlockAccountScheduling(account *Account, until time.Time, reason string) {
	r.accounts = append(r.accounts, account)
	r.until = append(r.until, until)
	r.reasons = append(r.reasons, reason)
}

func (r *runtimeBlockRecorder) ClearAccountSchedulingBlock(accountID int64) {
	r.clearedIDs = append(r.clearedIDs, accountID)
}

func TestRateLimitService_HandleUpstreamError_OpenAI403FirstHitTempUnschedulable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{1}}
	blocker := &runtimeBlockRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	service.SetAccountRuntimeBlocker(blocker)
	account := &Account{
		ID:       301,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"temporary edge rejection"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "temporary edge rejection")
	require.Contains(t, repo.lastTempReason, "(1/3)")
	require.Len(t, blocker.accounts, 1)
	require.Equal(t, account.ID, blocker.accounts[0].ID)
	require.Equal(t, "openai_403_temp", blocker.reasons[0])
	require.True(t, blocker.until[0].After(time.Now()))
}

func TestRateLimitService_HandleUpstreamError_OpenAI403ThresholdDisables(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	account := &Account{
		ID:       302,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"workspace forbidden by policy"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "workspace forbidden by policy")
	require.Contains(t, repo.lastErrorMsg, "consecutive_403=3/3")
}

func TestRateLimitService_HandleUpstreamError_Anthropic403ThresholdTempUnschedulable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{
		ID:       401,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	body := []byte(`{"type":"error","error":{"type":"permission_error","message":"OAuth token lacks required scopes"}}`)
	for i := 0; i < 2; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)
		require.False(t, shouldDisable)
		require.Equal(t, 0, repo.tempCalls)
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)
	require.True(t, shouldDisable)

	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, []int64{401, 401, 401}, counter.incrementIDs)
	require.Equal(t, []int{anthropicUpstreamErrorWindowMinutesDefault, anthropicUpstreamErrorWindowMinutesDefault, anthropicUpstreamErrorWindowMinutesDefault}, counter.windowMinutes)

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.lastTempReason), &state))
	require.Equal(t, http.StatusForbidden, state.StatusCode)
	require.Equal(t, "anthropic_upstream_error", state.MatchedKeyword)
	require.Contains(t, state.ErrorMessage, "OAuth token lacks required scopes")
}

// pool_mode Anthropic accounts go through the same 3/3 short-window counter
// as non-pool-mode accounts (2026-05-21 revision of PR #333). The blanket
// PR #333 immunity left ops with no mechanical signal that a stub was
// failing — its only slog.Warn had no alert hook, and the failover loop
// alone could not protect a single-member exclusive group from cascading
// customer-facing 503s. The replacement design uses tiered exponential
// cooldown (30s / 2min / 10min) so transient jitter is shrugged off in
// 30s while persistent failure still escalates to 10min.
//
// This test asserts pool_mode accounts:
//  1. DO feed the IncrementAnthropicUpstreamErrorCount counter.
//  2. DO write temp_unschedulable on the 3rd hit.
//  3. Use the tier-0 cooldown (30s) on the first cooldown in a window.
//
// Sibling test below (AnthropicCooldownTierEscalates) covers the 2nd/3rd
// tier escalation. Together they replace the prior
// AnthropicPoolModeBypassesUpstreamErrorCounter assertion that codified
// the now-removed blanket immunity.
func TestRateLimitService_HandleUpstreamError_AnthropicPoolModeStillCountsWithShortCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1, 2, 3},
		tierCounts: []int64{1},
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{
		ID:       402,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}

	body := []byte(`{"error":{"message":"upstream edge failed"}}`)
	for i := 0; i < 2; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, body)
		require.False(t, shouldDisable, "iteration %d: below threshold must not disable", i)
		require.Equal(t, 0, repo.tempCalls)
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, body)
	require.True(t, shouldDisable, "3rd hit must temp_unschedulable")

	require.Equal(t, 0, repo.setErrorCalls, "must not write account error state — temp_unschedulable only")
	require.Equal(t, 1, repo.tempCalls, "exactly one temp_unschedulable write on the 3rd hit")
	require.Equal(t, []int64{402, 402, 402}, counter.incrementIDs, "pool_mode account MUST feed the 3/3 counter")
	require.Equal(t, []int64{402}, counter.tierIncrementIDs, "tier counter incremented exactly once at threshold trip")

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.lastTempReason), &state))
	require.Equal(t, http.StatusBadGateway, state.StatusCode)
	require.Equal(t, "anthropic_upstream_error", state.MatchedKeyword)
	// First cooldown in the 30-min escalation window is the shortest tier (30s).
	// Margin allows for the 1-2ms scheduling delta between time.Now() in the
	// service and time.Now() in the test.
	untilDelta := time.Until(time.Unix(state.UntilUnix, 0))
	require.InDelta(t, 30*time.Second, untilDelta, float64(2*time.Second), "tier-0 cooldown must be 30s, got %s", untilDelta)
}

// Repeated cooldown trips within the escalation TTL window MUST escalate to
// the next tier in the ladder. Without this, persistent upstream failure
// would just bounce every 30s indefinitely, hammering the bad backend at
// ~50% error rate forever. The ladder ensures the 3rd+ trip lands at 10min.
func TestRateLimitService_HandleUpstreamError_AnthropicCooldownTierEscalates(t *testing.T) {
	tests := []struct {
		name             string
		tierCount        int64
		expectedCooldown time.Duration
		expectedTier     int
	}{
		{name: "tier_0_first_trip_30s", tierCount: 1, expectedCooldown: 30 * time.Second, expectedTier: 0},
		{name: "tier_1_second_trip_2min", tierCount: 2, expectedCooldown: 2 * time.Minute, expectedTier: 1},
		{name: "tier_2_third_trip_10min", tierCount: 3, expectedCooldown: 10 * time.Minute, expectedTier: 2},
		{name: "tier_clamps_above_ladder_len", tierCount: 10, expectedCooldown: 10 * time.Minute, expectedTier: 2},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			counter := &anthropicUpstreamErrorCounterCacheStub{
				counts:     []int64{3},
				tierCounts: []int64{tc.tierCount},
			}
			service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			service.SetAnthropicUpstreamErrorCounterCache(counter)
			account := &Account{
				ID:       500 + int64(tc.expectedTier),
				Platform: PlatformAnthropic,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"pool_mode": true,
				},
			}

			shouldDisable := service.HandleUpstreamError(
				context.Background(),
				account,
				http.StatusBadGateway,
				http.Header{},
				[]byte(`{"error":{"message":"upstream pool jitter"}}`),
			)
			require.True(t, shouldDisable)
			require.Equal(t, 1, repo.tempCalls)

			var state TempUnschedState
			require.NoError(t, json.Unmarshal([]byte(repo.lastTempReason), &state))
			untilDelta := time.Until(time.Unix(state.UntilUnix, 0))
			require.InDelta(t, tc.expectedCooldown, untilDelta, float64(2*time.Second),
				"tier=%d expected cooldown %s, got %s", tc.expectedTier, tc.expectedCooldown, untilDelta)
		})
	}
}

// Recovery paths must reset BOTH the short-window error counter AND the
// cooldown escalation tier so a healed account starts the next failure
// window at the shortest cooldown (30s) rather than carrying stale 10-min
// escalation state forward.
func TestRateLimitService_ResetAnthropicCounter_AlsoResetsCooldownTier(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	service.ResetAnthropicUpstreamErrorCounter(context.Background(), 600)
	require.Equal(t, []int64{600}, counter.resetCalls, "error counter reset must propagate")
	require.Equal(t, []int64{600}, counter.tierResetCalls, "cooldown tier reset must propagate")
}

// Carve-out: Anthropic accounts ignore the custom-error-codes allowlist so a
// non-listed 5xx still feeds the short-window counter. Without the
// `account.Platform != PlatformAnthropic` guard in HandleUpstreamError, an
// upstream merge that "simplifies" the early-return would silently drop the
// burst protection for any Anthropic APIKey customer who turned custom error
// codes on for, say, just 429.
func TestRateLimitService_HandleUpstreamError_AnthropicCustomErrorCodesStillCounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{
		ID:       403,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(429)},
		},
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadGateway,
		http.Header{},
		[]byte(`{"error":{"message":"upstream gateway timeout"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, []int64{403}, counter.incrementIDs)
}

// Recovery paths (ClearRateLimit / RecoverAccountAfterSuccessfulTest) must
// reset the Anthropic counter so a healed account does not carry stale strikes
// into the next short window. Mirrors the existing ResetOpenAI403Counter wiring.
func TestRateLimitService_ClearRateLimit_ResetsAnthropicCounter(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	require.NoError(t, service.ClearRateLimit(context.Background(), 404))
	require.Equal(t, []int64{404}, counter.resetCalls)
}

func TestRateLimitService_RecoverAccountAfterSuccessfulTest_ResetsAnthropicCounter(t *testing.T) {
	repo := &recoverableAccountRepoStub{
		rateLimitAccountRepoStub: rateLimitAccountRepoStub{},
		account: &Account{
			ID:       405,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusError,
		},
	}
	counter := &anthropicUpstreamErrorCounterCacheStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	result, err := service.RecoverAccountAfterSuccessfulTest(context.Background(), 405)
	require.NoError(t, err)
	require.True(t, result.ClearedError)
	require.Equal(t, []int64{405}, counter.resetCalls)
}

type recoverableAccountRepoStub struct {
	rateLimitAccountRepoStub
	account *Account
}

func (r *recoverableAccountRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.account, nil
}

func (r *recoverableAccountRepoStub) ClearError(ctx context.Context, id int64) error {
	r.account.Status = StatusActive
	return nil
}
