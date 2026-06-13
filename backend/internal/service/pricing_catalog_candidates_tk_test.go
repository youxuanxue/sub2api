//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TK (R-003, follow-up to PR #752): the admin model-whitelist selector now draws
// its candidates from tkServableCandidateIDs (self-healing) instead of the
// canonical defaults. These pin that membership truth on the Go side so the
// frontend no longer hand-maintains a hardcoded mirror.
func TestTkServableCandidateIDs(t *testing.T) {
	ctx := context.Background()

	contains := func(ids []string, want string) bool {
		for _, id := range ids {
			if id == want {
				return true
			}
		}
		return false
	}

	t.Run("anthropic draws from the empirically-servable allowlist (no fable-5 post-#752)", func(t *testing.T) {
		svc, _, _ := newAvailabilityTestService(t)
		ids := tkServableCandidateIDs(ctx, PlatformAnthropic, svc)
		require.True(t, contains(ids, "claude-opus-4-8"), "servable opus present")
		require.False(t, contains(ids, "claude-fable-5"), "access-gated fable-5 absent (W2 removed it from the allowlist)")
	})

	t.Run("structurally-gone model is pruned; degraded model stays", func(t *testing.T) {
		svc, repo, _ := newAvailabilityTestService(t)
		seedAvail(repo, PlatformAnthropic, "claude-opus-4-8", AvailabilityStatusUnreachable, FailureKindModelNotFound) // gone
		seedAvail(repo, PlatformAnthropic, "claude-sonnet-4-6", AvailabilityStatusUnreachable, FailureKindUpstream5xx)  // degraded
		ids := tkServableCandidateIDs(ctx, PlatformAnthropic, svc)
		require.False(t, contains(ids, "claude-opus-4-8"), "model_not_found→unreachable auto-drops (self-heal)")
		require.True(t, contains(ids, "claude-sonnet-4-6"), "transient 5xx-unreachable stays")
	})

	t.Run("antigravity keeps fable-5 (per-platform truth: servable there)", func(t *testing.T) {
		svc, _, _ := newAvailabilityTestService(t)
		ids := tkServableCandidateIDs(ctx, PlatformAntigravity, svc)
		require.True(t, contains(ids, "claude-fable-5"),
			"fable-5 gone on anthropic must NOT vanish from antigravity — the backend is the per-platform authority")
	})

	t.Run("nil availability degrades to no pruning (passthrough)", func(t *testing.T) {
		ids := tkServableCandidateIDs(ctx, PlatformAnthropic, nil)
		require.True(t, contains(ids, "claude-opus-4-8"), "without availability the full allowlist passes through")
	})
}
