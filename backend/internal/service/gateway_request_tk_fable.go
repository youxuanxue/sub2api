package service

import "strings"

// TokenKey: Fable-tier model predicates for thinking-shape rectification.
//
// Fable 5 (`claude-fable-5`) is the new tier above Opus. Captured real Claude
// Code 2.1.170 traffic (egress 3.148.79.145, 2026-06-09; see
// docs/spec-delta-cc-fable-5.md) shows Fable shares Opus 4.7+'s request surface:
// manual thinking (`thinking:{type:"enabled",budget_tokens:N}`) is rejected with
// the same adaptive-required 400, and only `{type:"adaptive"}` is accepted.
//
// The existing budget/adaptive rectifiers hard-gate on isOpus47OrNewer, whose
// name (and the shared claudeVersionRe regex) only matches opus/sonnet/haiku
// version strings — `claude-fable-5` matches neither, so without this widening
// Fable's adaptive-required 400 would be gated OUT of the reactive repair and
// surfaced raw to the client. requiresAdaptiveOnlyThinking is the broadened gate
// used at those call sites; isOpus47OrNewer stays honest to its name.

// isFableModel reports whether modelID is a Claude Fable-tier model. Matches the
// bare id, dated snapshots, the [1m] context-window alias, and the bedrock
// `anthropic.claude-fable-5` form via a single substring test.
func isFableModel(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "fable")
}

// requiresAdaptiveOnlyThinking reports whether the model rejects manual thinking
// (`thinking:{type:"enabled",budget_tokens:N}` → 400) and accepts only
// `{type:"adaptive"}`. True for Opus 4.7+ and all Fable-tier models. This is the
// platform-neutral gate; the bedrock path mirrors it via isBedrockOpus47OrNewer
// || isFableModel in sanitizeBedrockThinking.
func requiresAdaptiveOnlyThinking(modelID string) bool {
	return isOpus47OrNewer(modelID) || isFableModel(modelID)
}
