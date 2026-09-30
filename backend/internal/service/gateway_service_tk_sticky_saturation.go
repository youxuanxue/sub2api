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
	threshold, window := edgeMirrorStubSaturationThreshold, edgeMirrorStubSaturationWindowSeconds
	if isNewAPINVIDIABuildAccount(account) {
		threshold, window = nvidiaBuildInstabilityThreshold, nvidiaBuildInstabilityWindowSeconds
	}
	slog.Info("sticky_cleared_saturated_stub", "account_id", account.ID, "recent_count", count,
		"threshold", threshold, "window_seconds", window,
		"session", shortSessionHash(sessionHash))
	return true
}
