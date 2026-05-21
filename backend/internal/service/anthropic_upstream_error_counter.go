package service

import "context"

type AnthropicUpstreamErrorCounterCache interface {
	IncrementAnthropicUpstreamErrorCount(ctx context.Context, accountID int64, windowMinutes int) (int64, error)
	ResetAnthropicUpstreamErrorCount(ctx context.Context, accountID int64) error

	// Tier counter tracks how many cooldowns this account has triggered in the
	// recent past, so handleAnthropicUpstreamError can pick an exponentially
	// longer cooldown for persistent failure (30s → 2min → 10min) instead of
	// always applying the same fixed window. Reset paths mirror the error
	// counter so a healed account does not carry escalation state forward.
	IncrementAnthropicCooldownTier(ctx context.Context, accountID int64, ttlMinutes int) (int64, error)
	ResetAnthropicCooldownTier(ctx context.Context, accountID int64) error
}
