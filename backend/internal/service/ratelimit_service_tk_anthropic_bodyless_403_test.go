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

// At/above the bodyless-403 threshold, a persistent empty/unstructured Anthropic
// 403 must PERMANENTLY disable the account (SetError), NOT temp-cool — this is
// the empty-body org-ban that escapes #810's structured-body phrase breaker.
func TestRateLimitService_BodylessBan403_ThresholdPermanentlyDisables(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		bodyless403Counts: []int64{anthropic403BodylessDisableThresholdDefault},
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 901, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	shouldDisable := service.HandleUpstreamError(
		context.Background(), account, http.StatusForbidden, http.Header{}, []byte(""),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "threshold bodyless 403 must SetError (permanent)")
	require.Equal(t, 0, repo.tempCalls, "must NOT fall through to the auto-recovering temp cooldown")
	require.Contains(t, repo.lastErrorMsg, "Persistent bodyless")
	require.Equal(t, []int64{901}, counter.bodyless403IncrementIDs)
	require.Equal(t, []int{anthropic403BodylessWindowMinutesDefault}, counter.bodyless403WindowMin)
}

// Below threshold, a bodyless 403 must NOT permanently disable — it falls
// through to the existing 3/3 cooldown ladder unchanged (here the general
// counter is at its own threshold so we observe a temp_unschedulable write).
func TestRateLimitService_BodylessBan403_BelowThresholdFallsThroughToLadder(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		bodyless403Counts: []int64{anthropic403BodylessDisableThresholdDefault - 1},
		counts:            []int64{3}, // general 3/3 ladder at threshold → temp cool
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 902, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	shouldDisable := service.HandleUpstreamError(
		context.Background(), account, http.StatusForbidden, http.Header{}, []byte(""),
	)

	require.True(t, shouldDisable, "still fails over via the ladder")
	require.Equal(t, 0, repo.setErrorCalls, "below threshold must NOT permanently disable")
	require.Equal(t, 1, repo.tempCalls, "falls through to the transient cooldown ladder")
	require.Equal(t, []int64{902}, counter.bodyless403IncrementIDs, "bodyless counter still advanced")
}

// A structured-body 403 (e.g. a model-level denial naming the model) must NOT
// touch the bodyless counter or escalate — that is exactly the false-disable
// #810's precision bar guards against.
func TestRateLimitService_BodylessBan403_StructuredBodyNeverCounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		bodyless403Counts: []int64{anthropic403BodylessDisableThresholdDefault}, // would disable IF counted
		counts:            []int64{1},
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 903, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	body := []byte(`{"type":"error","error":{"type":"permission_error","message":"you do not have access to this model"}}`)
	service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	require.Equal(t, 0, repo.setErrorCalls, "structured model-level 403 must never permanently disable")
	require.Empty(t, counter.bodyless403IncrementIDs, "structured body must not advance the bodyless counter")
}

// During a live Claude API incident, bodyless 403 at threshold still permanently disables.
func TestRateLimitService_BodylessBan403_IncidentStillEscalates(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{IsIncident: true, Status: "partial_outage", FetchedAt: time.Now()})

	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		bodyless403Counts: []int64{anthropic403BodylessDisableThresholdDefault},
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 904, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(""))

	require.Equal(t, 1, repo.setErrorCalls, "incident must not skip bodyless 403 permanent disable")
	require.Equal(t, []int64{904}, counter.bodyless403IncrementIDs)
}

// A counter backend error must fail OPEN — never permanently disable on a
// telemetry failure. The request still falls through to the existing ladder.
func TestRateLimitService_BodylessBan403_CounterErrorFailsOpen(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{err: context.DeadlineExceeded}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 905, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(""))

	require.Equal(t, 0, repo.setErrorCalls, "counter error must fail open, never permanently disable")
}

// Recovery resets the bodyless-403 strike counter (folded into
// ResetAnthropicUpstreamErrorCounter, which every recovery hotpath invokes).
func TestRateLimitService_ResetAnthropicCounter_AlsoResetsBodyless403(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	service.ResetAnthropicUpstreamErrorCounter(context.Background(), 906)
	require.Equal(t, []int64{906}, counter.bodyless403ResetCalls, "bodyless 403 counter reset must propagate")
}
