package service

import "context"

// AnthropicCooldownTierEscalationsKey is the Redis key used to aggregate
// "tier >= 1" cooldown events across all Anthropic accounts. Exported so
// the ops alert evaluator can read it without going through the cache
// interface (which would require wiring the interface into the evaluator
// constructor / Wire DI graph). The repository INCR/GET helpers and the
// evaluator metric MUST agree on this key.
const AnthropicCooldownTierEscalationsKey = "anthropic_cooldown_tier_escalations:global"

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

	// Global escalation counter aggregates "tier >= 1" cooldown events across
	// all Anthropic accounts in a rolling window (TTL controlled by caller).
	// It exists so ops_alert_evaluator can drive a metric/alert without
	// scanning every per-account counter. Increment is best-effort — a Redis
	// failure only loses telemetry resolution and never blocks request
	// handling. Read returns 0 when the key has expired (no escalation in
	// the most recent window) or the cache backend is unhealthy.
	IncrementAnthropicCooldownTierEscalations(ctx context.Context, ttlMinutes int) (int64, error)
	GetAnthropicCooldownTierEscalations(ctx context.Context) (int64, error)
}
