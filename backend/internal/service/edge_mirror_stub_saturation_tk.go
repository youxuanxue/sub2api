package service

// Shared constants for OpenAI/Anthropic soft scheduling preference.
//
// Anthropic, OpenAI-compatible and Antigravity relays share these constants.
// OpenAI also reuses them for native capacity 503 on edge OAuth / setup-token
// accounts (see openai_capacity_saturation_tk.go). candidate_saturation.go owns
// counter scope and interpretation for group choice, account scoring and sticky
// eviction. Antigravity counters use account plus resolved upstream model; the
// other counters retain account scope.
//
//   - increment on classified downstream-capacity skip paths and OpenAI native
//     capacity 503, without advancing the cooldown ladder
//   - at threshold: clear sticky + scheduler preference penalty
//   - prod mirror/relay stubs never write model_rate_limits — edge OAuth owns quota truth
//   - self-clearing as individual events leave the rolling window
//
// Intentionally NEVER on this path: SetTempUnschedulable, whole-account
// SetRateLimited, or per-failover in-memory BlockAccountScheduling — those
// collapsed pools in prod (2026-05-31 amplifier) or over-penalize transient blips.

const (
	edgeMirrorStubSaturationWindowSeconds = 600

	// edgeMirrorStubSaturationThreshold: transient blips stay on the account;
	// sustained capacity failures in the rolling window trigger preference.
	edgeMirrorStubSaturationThreshold int64 = 5

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
