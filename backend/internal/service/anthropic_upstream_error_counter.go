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

	// Escalation slot is a per-account guard that lets the tier ladder escalate
	// at most once per *failure episode* instead of once per error. A single
	// fast burst — e.g. an edge rolling-upgrade swap window throwing several
	// 503s in a few seconds (issue #623) — would otherwise re-run the threshold
	// block per error and climb 30s → 2min → 10min within seconds, even though
	// errors #2..n are racing in-flight requests from the SAME episode that
	// error #1 already cooled the account for.
	//
	// AcquireAnthropicCooldownEscalationSlot does an atomic SET-if-absent with a
	// placeholder TTL and returns whether THIS caller won the slot. The winner
	// performs the escalation, then calls SetAnthropicCooldownEscalationSlotTTL
	// to shrink the slot to exactly the cooldown it applied, so the slot
	// auto-clears the moment the account would be rescheduled — a genuine
	// re-trip after the cooldown expires is a new episode and escalates again
	// (the ladder's documented "repeatedly trips ... inside 30 min" intent).
	// Losers fail the in-flight request over without advancing the tier.
	// Both are best-effort: a Redis failure must never under-protect a genuine
	// persistent failure, so the caller falls back to escalating on error.
	AcquireAnthropicCooldownEscalationSlot(ctx context.Context, accountID int64, ttlSeconds int) (bool, error)
	SetAnthropicCooldownEscalationSlotTTL(ctx context.Context, accountID int64, ttlSeconds int) error
	ResetAnthropicCooldownEscalationSlot(ctx context.Context, accountID int64) error
}
