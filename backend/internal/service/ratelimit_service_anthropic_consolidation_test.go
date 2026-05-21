//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// PR #338 (consolidation): tests covering the four behavioral changes layered
// on top of PR #337's cooldown ladder.
//
// P3: 429 with an upstream-provided reset header (anthropic-ratelimit-unified-
//     reset) must SetRateLimited precisely AND suppress the ladder's
//     SetTempUnschedulable so last-write-wins doesn't replace the upstream-
//     authoritative reset time with a less-precise local cooldown. The
//     counters still advance.
// P3: 529 with a successful SetOverloaded write must likewise suppress the
//     ladder cooldown write.
// P4: A second 403 hit whose prior reason was also a 403 temp-unschedulable
//     must escalate (tryTempUnschedulable returns false) so handle403's
//     downstream Anthropic path runs without writing yet another 6h cooldown.
// P4: A 403 body matching tlsFingerprintFailureKeywords must apply a 30s
//     cooldown (not 6h) so an operator can re-capture the CLI TLS profile
//     before the next attempt — the long cooldown would mask a basic-
//     infrastructure failure as an account-level one.
// P5: cfg.RateLimit.AnthropicErrorThreshold lifts the 3/3 short-window
//     threshold without requiring a recompile (motivation: single-account /
//     small-pool deployments where Sonnet↔Opus burst jitter trips 3/3 too
//     easily).
// P2: tier >= 1 cooldown trips emit the global escalation counter so
//     ops_alert_evaluator can drive a "persistent failure rising" alert
//     without scanning every per-account key.

// --- P3: 429 retry-after precision suppresses ladder cooldown -----------------

func TestRateLimitService_HandleUpstreamError_Anthropic429PreciseResetSuppressesLadder(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1, 2, 3},
		tierCounts: []int64{1},
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       701,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	resetAt := time.Now().Add(45 * time.Minute).Unix()
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-reset", strconv.FormatInt(resetAt, 10))

	for i := 0; i < 3; i++ {
		shouldDisable := service.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusTooManyRequests,
			headers,
			[]byte(`{"error":{"type":"rate_limit_error","message":"limit reached"}}`),
		)
		// First two hits: counter < threshold, ladder returns false.
		// Third hit: counter == threshold, ladder runs but the SetRateLimited
		// already landed first, so handleAnthropicUpstreamErrorWithOptions
		// receives skipCooldownWrite=true and still returns true.
		if i < 2 {
			require.False(t, shouldDisable, "hit %d should not have disabled", i+1)
		} else {
			require.True(t, shouldDisable, "third hit should have returned true")
		}
	}

	require.Equal(t, 3, repo.setRateLimitedCalls, "every 429 with precise reset hits SetRateLimited")
	require.Equal(t, 0, repo.tempCalls,
		"ladder must NOT write SetTempUnschedulable when SetRateLimited landed (last-write-wins protection)")
	require.Equal(t, []int64{701, 701, 701}, counter.incrementIDs,
		"3/3 short-window counter advanced on every 429")
}

// 429 without an upstream reset header falls back to handle429's
// apply429FallbackRateLimit; that path is NOT considered an "authoritative
// upstream cooldown" so the ladder cooldown write SHOULD still happen
// (otherwise we lose the only signal that the account is failing).
func TestRateLimitService_HandleUpstreamError_Anthropic429NoResetHeaderStillLadders(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1, 2, 3},
		tierCounts: []int64{1},
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       702,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	for i := 0; i < 3; i++ {
		service.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusTooManyRequests,
			http.Header{}, // no reset header
			[]byte(`{"error":{"message":"some other 429 without reset hint"}}`),
		)
	}

	require.Equal(t, 1, repo.tempCalls,
		"third 429 without reset header MUST land the ladder cooldown — that's the only signal of a failing account")
}

// --- P3: 529 successful overload suppresses ladder cooldown -------------------

func TestRateLimitService_HandleUpstreamError_Anthropic529SetOverloadedSuppressesLadder(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1, 2, 3},
		tierCounts: []int64{1},
	}
	service := NewRateLimitService(repo, nil, &config.Config{
		RateLimit: config.RateLimitConfig{
			OverloadCooldownMinutes: 10,
		},
	}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       703,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	for i := 0; i < 3; i++ {
		service.HandleUpstreamError(
			context.Background(),
			account,
			529,
			http.Header{},
			[]byte(`{"error":{"type":"overloaded_error","message":"upstream overloaded"}}`),
		)
	}

	require.Equal(t, 3, repo.setOverloadedCalls, "every 529 wrote SetOverloaded")
	require.Equal(t, 0, repo.tempCalls,
		"ladder must NOT write SetTempUnschedulable when SetOverloaded landed")
}

// --- P4: 403 second hit escalates ---------------------------------------------

func TestRateLimitService_TryTempUnschedulable_403SecondHitEscalates(t *testing.T) {
	prior := &TempUnschedState{
		UntilUnix:       time.Now().Add(6 * time.Hour).Unix(),
		TriggeredAtUnix: time.Now().Add(-5 * time.Minute).Unix(),
		StatusCode:      http.StatusForbidden,
		MatchedKeyword:  "account_disabled_auth_error",
	}
	priorJSON, err := json.Marshal(prior)
	require.NoError(t, err)

	repo := &rateLimitAccountRepoStub{
		tempReasonOnGet: string(priorJSON),
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	account := &Account{
		ID:                      810,
		Platform:                PlatformAnthropic,
		Type:                    AccountTypeOAuth,
		TempUnschedulableReason: string(priorJSON),
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []map[string]any{
				{
					"error_code":       403,
					"keywords":         []string{"account_disabled_auth_error"},
					"duration_minutes": 360,
				},
			},
		},
	}

	body := []byte(`{"error":{"type":"forbidden","message":"account_disabled_auth_error"}}`)
	matched := service.HandleTempUnschedulable(context.Background(), account, http.StatusForbidden, body)
	require.False(t, matched,
		"second 403 with same status_code on prior temp-unschedulable must NOT re-arm the 6h cooldown")
}

// --- P4: TLS fingerprint failure path -----------------------------------------

func TestRateLimitService_HandleUpstreamError_Anthropic403TLSFingerprintShortCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       820,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	body := []byte(`{"error":{"message":"forbidden: JA3 fingerprint does not match expected Claude CLI shape"}}`)
	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		body,
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.tempCalls,
		"TLS fingerprint failure should write a SetTempUnschedulable cooldown immediately")

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.lastTempReason), &state))
	require.Equal(t, http.StatusForbidden, state.StatusCode)
	require.Equal(t, "tls_fingerprint_failure", state.MatchedKeyword)

	cooldown := time.Until(time.Unix(state.UntilUnix, 0))
	require.InDelta(t, tlsFingerprintFailureCooldown, cooldown, float64(2*time.Second),
		"TLS fingerprint failure must use the short 30s cooldown, NOT the 6h account-disabled cooldown")
	require.Zero(t, len(counter.incrementIDs),
		"TLS fingerprint path must NOT feed the 3/3 short-window counter (the account is fine; infra is not)")
}

// Production OAuth accounts ship with the baseline credentials wired —
// temp_unschedulable_enabled=true + a 403 account_disabled_auth_error rule
// (anthropic-oauth-stability-baselines-tiered.json shared_baseline.credentials).
// A 403 body that contains a TLS-fingerprint keyword but NOT
// account_disabled_auth_error MUST still reach the handle403 TLS branch and
// land the short 30s cooldown — the JSON 403 rule MUST NOT eat the request
// just because the rule's error_code matches. Regression coverage for the
// "production credentials path" vs the simpler bare-account test above.
func TestRateLimitService_HandleUpstreamError_Anthropic403TLSFingerprintNotEatenBy403Rule(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       821,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []map[string]any{
				{
					"error_code":       403,
					"keywords":         []string{"account_disabled_auth_error", "organization disabled"},
					"duration_minutes": 360,
				},
			},
		},
	}

	body := []byte(`{"error":{"message":"forbidden: JA4 fingerprint mismatch on this client"}}`)
	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		body,
	)
	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.tempCalls,
		"TLS fingerprint failure must still write SetTempUnschedulable even with the 403 rule armed")

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.lastTempReason), &state))
	require.Equal(t, "tls_fingerprint_failure", state.MatchedKeyword,
		"reason must record the TLS path, NOT the 6h account_disabled_auth_error rule")

	cooldown := time.Until(time.Unix(state.UntilUnix, 0))
	require.InDelta(t, tlsFingerprintFailureCooldown, cooldown, float64(2*time.Second),
		"production credentials path must still apply 30s cooldown, not 6h")
}

// --- P5: cfg.AnthropicErrorThreshold lifts the 3/3 bar -----------------------

func TestRateLimitService_HandleUpstreamError_AnthropicErrorThresholdConfigurable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1, 2, 3, 4, 5},
		tierCounts: []int64{1},
	}
	service := NewRateLimitService(repo, nil, &config.Config{
		RateLimit: config.RateLimitConfig{
			AnthropicErrorThreshold:     5,
			AnthropicErrorWindowMinutes: 2,
		},
	}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       930,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	body := []byte(`{"error":{"message":"upstream pool jitter"}}`)
	for i := 0; i < 4; i++ {
		shouldDisable := service.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusBadGateway,
			http.Header{},
			body,
		)
		require.False(t, shouldDisable,
			"hit %d below threshold=5 must not have disabled", i+1)
	}
	require.Equal(t, 0, repo.tempCalls)

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadGateway,
		http.Header{},
		body,
	)
	require.True(t, shouldDisable, "fifth hit must trip the configured threshold=5")
	require.Equal(t, 1, repo.tempCalls)

	require.Equal(t, []int{2, 2, 2, 2, 2}, counter.windowMinutes,
		"counter must call IncrementAnthropicUpstreamErrorCount with the configured window_minutes=2")
}

func TestRateLimitService_HandleUpstreamError_AnthropicErrorThresholdDefaultsWhenUnset(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1, 2, 3},
		tierCounts: []int64{1},
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil) // cfg unset
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       931,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	for i := 0; i < 3; i++ {
		service.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusBadGateway,
			http.Header{},
			[]byte(`{"error":{"message":"jitter"}}`),
		)
	}
	require.Equal(t, 1, repo.tempCalls,
		"unset config must fall back to the built-in default threshold=3")
}

// --- P2: tier >= 1 emits the global escalation counter signal ---------------

func TestRateLimitService_HandleUpstreamError_AnthropicTier0DoesNotEmitEscalation(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{3},
		tierCounts: []int64{1}, // first trip → tier 0 (30s, transient jitter)
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       1040,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadGateway,
		http.Header{},
		[]byte(`{"error":{"message":"transient jitter"}}`),
	)

	require.Zero(t, len(counter.escalationTTLMinutes),
		"tier=0 (transient jitter) must NOT increment the global escalation counter")
}

func TestRateLimitService_HandleUpstreamError_AnthropicTier1EmitsEscalation(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{3},
		tierCounts: []int64{2}, // second trip → tier 1 (2 min, real problem starting)
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       1041,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadGateway,
		http.Header{},
		[]byte(`{"error":{"message":"persistent failure"}}`),
	)

	require.Equal(t, 1, len(counter.escalationTTLMinutes),
		"tier=1 must increment the global escalation counter exactly once")
	require.Equal(t, anthropicCooldownTierEscalationsWindowMinutes, counter.escalationTTLMinutes[0],
		"escalation TTL must match the documented 60-min ops window")
}

// Sanity: the stub-backed Get returns the running total so an evaluator-style
// reader sees writes in the same in-process test.
func TestAnthropicUpstreamErrorCounterCacheStub_EscalationsReadConsistentWithWrites(t *testing.T) {
	counter := &anthropicUpstreamErrorCounterCacheStub{}
	ctx := context.Background()

	got, err := counter.GetAnthropicCooldownTierEscalations(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), got)

	_, err = counter.IncrementAnthropicCooldownTierEscalations(ctx, 60)
	require.NoError(t, err)
	_, err = counter.IncrementAnthropicCooldownTierEscalations(ctx, 60)
	require.NoError(t, err)

	got, err = counter.GetAnthropicCooldownTierEscalations(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), got)
}

// End-to-end shape: the evaluator's anthropic_cooldown_tier_escalation_count
// metric path reads exactly what handleAnthropicUpstreamErrorWithOptions
// wrote through the stub-backed cache, so a sustained tier>=1 condition
// drives the rule metric to the same number ops dashboards will display.
func TestOpsAlertEvaluator_AnthropicCooldownTierEscalationCount_ReflectsLadderWrites(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{3, 3},
		tierCounts: []int64{2, 3}, // both trips land at tier>=1
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{
		ID:       1050,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	for i := 0; i < 2; i++ {
		service.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusBadGateway,
			http.Header{},
			[]byte(`{"error":{"message":"persistent failure"}}`),
		)
	}

	evaluator := &OpsAlertEvaluatorService{}
	evaluator.SetAnthropicUpstreamErrorCounterCache(counter)

	rule := &OpsAlertRule{MetricType: "anthropic_cooldown_tier_escalation_count"}
	value, ok := evaluator.computeRuleMetric(
		context.Background(),
		rule,
		nil,
		time.Now().UTC().Add(-time.Hour),
		time.Now().UTC(),
		"",
		nil,
	)
	require.True(t, ok, "metric must be reportable when the cache is wired")
	require.InDelta(t, 2.0, value, 0.0001,
		"two tier>=1 trips must surface as escalation_count=2 to ops alerts")
}

