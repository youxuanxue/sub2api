package service

import (
	"context"
	"log/slog"
)

// NVIDIA Build is secondary, empirically unstable capacity. After any upstream
// error we soft-deprioritize the account in selection so the next request
// prefers volcengine/qianfan siblings instead of paying another failover hop.
//
// Preference is Redis-backed only (same OpenAI saturation counter, NVIDIA
// window). When the counter is unwired or Increment fails, this request still
// failovers; subsequent selection keeps base order until Redis recovers.
// Soft preference never SetError / ladder-advances from this path.
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
// for a NVIDIA Build account. Returns 0 when Redis is unwired or Increment fails.
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
