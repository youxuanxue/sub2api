package service

// Shared constants for prod edge-mirror downstream-empty preference.
//
// Anthropic, OpenAI-compatible and Antigravity relays share these constants.
// candidate_saturation.go owns counter scope and interpretation for group choice,
// account scoring and sticky eviction. Antigravity counters use account plus
// resolved upstream model; the other counters retain account scope.
//
//   - increment on classified downstream-capacity skip paths, without advancing the cooldown ladder
//   - at threshold: clear sticky + scheduler preference penalty
//   - prod mirror/relay stubs never write model_rate_limits — edge OAuth owns quota truth
//   - self-clearing via window TTL (90s after the first hit in a fixed window)
//
// Intentionally NEVER on this path: SetTempUnschedulable, whole-account
// SetRateLimited, or per-failover in-memory BlockAccountScheduling — those
// collapsed pools in prod (2026-05-31 amplifier) or over-penalize transient blips.

const (
	edgeMirrorStubSaturationWindowSeconds = 90

	// edgeMirrorStubSaturationThreshold: transient blips (1–2 hits) stay on the
	// stub; sustained downstream-empty (≥3 in 90s) triggers preference only.
	edgeMirrorStubSaturationThreshold int64 = 3

	// anthropicSaturationPriorityPenalty is the shared additive priority penalty
	// in the generic gateway's legacy and load-aware selectors.
	anthropicSaturationPriorityPenalty = 1000

	// openAISaturationScorePenalty is subtracted from weighted LB score (~0..5).
	openAISaturationScorePenalty = 50.0
)

// Compatibility aliases still used by platform-specific writers and tests.
const (
	anthropicSaturationThreshold            = edgeMirrorStubSaturationThreshold
	openAIEdgeMirrorStubSaturationThreshold = edgeMirrorStubSaturationThreshold
)
