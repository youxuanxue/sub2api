package service

import (
	"context"
	"log/slog"
	"time"
)

// NVIDIA Build is secondary, empirically unstable capacity. After any upstream
// error we soft-deprioritize the account in selection so the next request
// prefers volcengine/qianfan siblings instead of paying another failover hop.
//
// Preference is soft when the Redis saturation counter is wired. When Redis is
// nil or Increment fails, a process-local expiry map still deprioritizes so the
// scheduler cannot silently keep preferring a failing NVIDIA account.
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

// nvidiaLocalInstabilitySource exposes process-local NVIDIA soft-preference
// counts to candidateSaturationState when Redis is unavailable.
type nvidiaLocalInstabilitySource interface {
	nvidiaLocalInstabilityCount(accountID int64) int64
}

// recordNVIDIABuildInstability increments the rolling soft-preference counter
// for a NVIDIA Build account. Redis is preferred; local fallback guarantees
// selection still sees threshold-1 pressure when the counter is unwired.
func (s *RateLimitService) recordNVIDIABuildInstability(ctx context.Context, accountID int64, statusCode int) int64 {
	if s == nil {
		return 0
	}
	if s.openaiSaturationCounter != nil {
		count, err := s.openaiSaturationCounter.IncrementSaturation(ctx, accountID, nvidiaBuildInstabilityWindowSeconds)
		if err == nil {
			s.noteNVIDIALocalInstability(accountID)
			if count == nvidiaBuildInstabilityThreshold {
				slog.Info("nvidia_build_instability_deprioritized",
					"account_id", accountID,
					"recent_count", count,
					"threshold", nvidiaBuildInstabilityThreshold,
					"window_seconds", nvidiaBuildInstabilityWindowSeconds,
					"status_code", statusCode,
					"source", "redis")
			}
			return count
		}
		slog.Warn("nvidia_build_instability_increment_failed",
			"account_id", accountID,
			"status_code", statusCode,
			"error", err)
	}
	s.noteNVIDIALocalInstability(accountID)
	slog.Info("nvidia_build_instability_deprioritized",
		"account_id", accountID,
		"recent_count", nvidiaBuildInstabilityThreshold,
		"threshold", nvidiaBuildInstabilityThreshold,
		"window_seconds", nvidiaBuildInstabilityWindowSeconds,
		"status_code", statusCode,
		"source", "local")
	return nvidiaBuildInstabilityThreshold
}

func (s *RateLimitService) noteNVIDIALocalInstability(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.nvidiaLocalInstabilityMu.Lock()
	defer s.nvidiaLocalInstabilityMu.Unlock()
	if s.nvidiaLocalInstabilityUntil == nil {
		s.nvidiaLocalInstabilityUntil = make(map[int64]time.Time)
	}
	s.nvidiaLocalInstabilityUntil[accountID] = time.Now().Add(time.Duration(nvidiaBuildInstabilityWindowSeconds) * time.Second)
}

func (s *RateLimitService) nvidiaLocalInstabilityCount(accountID int64) int64 {
	if s == nil || accountID <= 0 {
		return 0
	}
	s.nvidiaLocalInstabilityMu.Lock()
	defer s.nvidiaLocalInstabilityMu.Unlock()
	until, ok := s.nvidiaLocalInstabilityUntil[accountID]
	if !ok {
		return 0
	}
	if time.Now().After(until) {
		delete(s.nvidiaLocalInstabilityUntil, accountID)
		return 0
	}
	return nvidiaBuildInstabilityThreshold
}
