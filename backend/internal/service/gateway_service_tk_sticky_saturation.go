package service

import (
	"context"
	"log/slog"
)

// tkShouldClearStickyForSaturation releases affinity at the same threshold as account scoring.
// It does not remove the account from the candidate pool.
func (s *GatewayService) tkShouldClearStickyForSaturation(ctx context.Context, account *Account, sessionHash string, requestedModel ...string) bool {
	if s == nil || account == nil {
		return false
	}
	count := s.candidateSaturationState().counts(ctx, []*Account{account}, firstRequestedModel(requestedModel))[account.ID]
	if !candidateSaturatedFor(account, count) {
		return false
	}
	if isNewAPINVIDIABuildAccount(account) {
		slog.Info("nvidia_build_sticky_cleared_instability",
			"account_id", account.ID,
			"recent_count", count,
			"threshold", nvidiaBuildInstabilityThreshold,
			"window_seconds", nvidiaBuildInstabilityWindowSeconds,
			"session", shortSessionHash(sessionHash))
		return true
	}
	slog.Info("anthropic_sticky_cleared_saturated_stub", "account_id", account.ID, "recent_count", count,
		"threshold", edgeMirrorStubSaturationThreshold, "window_seconds", edgeMirrorStubSaturationWindowSeconds,
		"session", shortSessionHash(sessionHash))
	return true
}
