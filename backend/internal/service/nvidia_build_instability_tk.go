package service

import (
	"context"
	"log/slog"
)

// NVIDIA Build is secondary, empirically unstable capacity. After any upstream
// error we soft-deprioritize the account in selection (same Redis saturation
// counter family as OpenAI edge-mirror stubs) so the next request prefers
// volcengine/qianfan siblings instead of paying another failover hop.
//
// Soft preference only: never SetTempUnschedulable / SetError / ladder advance
// from this path. Window self-clears; threshold is 1 so the first blip moves
// traffic away immediately (prod 2026-09-29 opaque-404 storm).
const (
	nvidiaBuildInstabilityWindowSeconds = 300

	// First upstream blip moves traffic to siblings; window self-clears.
	nvidiaBuildInstabilityThreshold int64 = 1
)

func candidateSaturatedFor(account *Account, count int64) bool {
	if isNewAPINVIDIABuildAccount(account) {
		return count >= nvidiaBuildInstabilityThreshold
	}
	return candidateSaturated(count)
}

// recordNVIDIABuildInstability increments the rolling soft-preference counter
// for a NVIDIA Build account. Best-effort: Redis errors must not break failover.
func (s *RateLimitService) recordNVIDIABuildInstability(ctx context.Context, accountID int64, statusCode int) int64 {
	if s == nil || s.openaiSaturationCounter == nil {
		return 0
	}
	count, err := s.openaiSaturationCounter.IncrementSaturation(ctx, accountID, nvidiaBuildInstabilityWindowSeconds)
	if err != nil {
		slog.Warn("nvidia_build_instability_increment_failed",
			"account_id", accountID,
			"status_code", statusCode,
			"error", err)
		return 0
	}
	if count == nvidiaBuildInstabilityThreshold {
		slog.Info("nvidia_build_instability_deprioritized",
			"account_id", accountID,
			"recent_count", count,
			"threshold", nvidiaBuildInstabilityThreshold,
			"window_seconds", nvidiaBuildInstabilityWindowSeconds,
			"status_code", statusCode)
	}
	return count
}
