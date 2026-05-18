package service

import "context"

// AnthropicSignaturePreemptCache tracks per-account thinking-block signature_error
// bursts and exposes a "preempt cooldown" flag. While the flag is armed, the
// gateway pre-filters thinking blocks before the first upstream call, skipping
// the otherwise-guaranteed 400 + signature_error round trip.
type AnthropicSignaturePreemptCache interface {
	// ArmIfThreshold records one signature_error for accountID. The counter has
	// a sliding-window TTL of windowSeconds. When the in-window count reaches
	// threshold the preempt flag is set (TTL=cooldownSeconds). Returns the new
	// count, armedNow=true iff THIS call transitioned the flag from unset to set.
	ArmIfThreshold(ctx context.Context, accountID int64, threshold, windowSeconds, cooldownSeconds int) (count int64, armedNow bool, err error)

	// IsArmed returns true while the preempt flag is in effect.
	IsArmed(ctx context.Context, accountID int64) (bool, error)

	// Reset clears both the counter and the preempt flag for accountID.
	Reset(ctx context.Context, accountID int64) error
}
