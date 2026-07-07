package service

// SSOT — prod edge-mirror stub downstream-empty de-prioritization.
//
// Both Anthropic (cc-us* → edge OAuth) and OpenAI (openai-us* → api-us*.tokenkey.dev)
// share the same counter window, saturation threshold, and preference model:
//
//   - increment on downstream-capacity skip path only (never handle429 / ladder)
//   - at threshold: clear sticky + scheduler preference penalty
//   - at threshold: mirror model/class-scoped cooldown write (see mirror *_429.go)
//   - self-clearing via window TTL (~90s after last hit)
//
// Intentionally NEVER on this path: SetTempUnschedulable, whole-account
// SetRateLimited, or per-failover in-memory BlockAccountScheduling — those
// collapsed pools in prod (2026-05-31 amplifier) or over-penalize transient blips.

const (
	edgeMirrorStubSaturationWindowSeconds = 90

	// edgeMirrorStubSaturationThreshold: transient blips (1–2 hits) stay on the
	// stub; sustained downstream-empty (≥3 in 90s) triggers preference + mirror
	// model/class write. Shared by Anthropic and OpenAI.
	edgeMirrorStubSaturationThreshold int64 = 3

	// anthropicSaturationPriorityPenalty is added to effectivePriority in the
	// Anthropic load-aware scheduler (priorities are small ints ~0..100).
	anthropicSaturationPriorityPenalty = 1000

	// openAISaturationScorePenalty is subtracted from weighted LB score (~0..5).
	openAISaturationScorePenalty = 50.0
)

// Legacy aliases keep anthropic/openai call sites and sentinels stable.
const (
	anthropicSaturationWindowSeconds           = edgeMirrorStubSaturationWindowSeconds
	anthropicSaturationThreshold               = edgeMirrorStubSaturationThreshold
	anthropicEdgeMirrorStubSaturationThreshold = edgeMirrorStubSaturationThreshold
	openAIEdgeMirrorStubSaturationThreshold    = edgeMirrorStubSaturationThreshold
	tkAnthropicMirrorClassCooldownSeconds      = edgeMirrorStubSaturationWindowSeconds
)
