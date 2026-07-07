package service

// SSOT — prod edge-mirror stub downstream-empty de-prioritization.
//
// Both Anthropic (cc-us* → edge OAuth) and OpenAI (openai-us* → api-us*.tokenkey.dev)
// share the same counter window and preference model:
//
//   - increment on downstream-capacity skip path only (never handle429 / ladder)
//   - at platform threshold: clear sticky + scheduler preference penalty
//   - self-clearing via window TTL (~90s after last hit)
//
// Anthropic-only write side: class-scoped mirror cooldown at the anthropic threshold
// (ratelimit_service_tk_mirror_class_429.go). OpenAI intentionally has NO
// temp_unschedulable / per-failover runtime block — preference layer only.

const (
	edgeMirrorStubSaturationWindowSeconds = 90

	anthropicEdgeMirrorStubSaturationThreshold int64 = 4
	openAIEdgeMirrorStubSaturationThreshold    int64 = 3

	// anthropicSaturationPriorityPenalty is added to effectivePriority in the
	// Anthropic load-aware scheduler (priorities are small ints ~0..100).
	anthropicSaturationPriorityPenalty = 1000

	// openAISaturationScorePenalty is subtracted from weighted LB score (~0..5).
	openAISaturationScorePenalty = 50.0
)

// Legacy aliases keep anthropic call sites and sentinels stable.
const (
	anthropicSaturationWindowSeconds      = edgeMirrorStubSaturationWindowSeconds
	anthropicSaturationThreshold          = anthropicEdgeMirrorStubSaturationThreshold
	tkAnthropicMirrorClassCooldownSeconds = edgeMirrorStubSaturationWindowSeconds
)
