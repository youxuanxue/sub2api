package service

import (
	"context"
)

// Generic gateway load-aware scoring consumes candidate_saturation.go, including
// mixed pools and model-scoped Antigravity relays. The bounded additive penalty
// keeps saturated last resorts selectable and preserves all-saturated order.
// Constants live in edge_mirror_stub_saturation_tk.go; counter scope, settings and
// read-failure behavior belong to the shared state owner.

// SetAnthropicSaturationCounter wires the Redis-backed saturation counter into
// GatewayService post-construction (mirrors SetAnthropicSigPreemptCache). Nil-
// safe: when unset, Anthropic counts are absent; other wired platform counters
// still participate through candidateSaturationState.
func (s *GatewayService) SetAnthropicSaturationCounter(cache AnthropicSaturationCounterCache) {
	if s != nil {
		s.tkAnthropicSaturationCounter = cache
	}
}

// HasAnthropicSaturationCounter reports whether the saturation counter is wired
// (used by DI smoke tests to prove the post-construction setter ran).
func (s *GatewayService) HasAnthropicSaturationCounter() bool {
	return s != nil && s.tkAnthropicSaturationCounter != nil
}

// computeAnthropicSaturationPenalties applies the shared live state to priority.
func (s *GatewayService) computeAnthropicSaturationPenalties(ctx context.Context, candidates []accountWithLoad, requestedModel ...string) {
	if s == nil {
		return
	}
	accounts := make([]*Account, 0, len(candidates))
	for _, candidate := range candidates {
		accounts = append(accounts, candidate.account)
	}
	counts := s.candidateSaturationState().counts(ctx, accounts, firstRequestedModel(requestedModel))
	for i := range candidates {
		if candidates[i].account != nil && candidateSaturated(counts[candidates[i].account.ID]) {
			candidates[i].saturationPenalty = anthropicSaturationPriorityPenalty
		}
	}
}

// effectivePriority is the ranking key used by filterByMinPriority: the base
// account priority plus the bounded saturation penalty (0 when the feature is
// off / the stub is not saturated). Keeping this as a tiny helper means
// filterByMinPriority reads as "min effective priority" with the TK term folded
// in, rather than scattering the penalty arithmetic across the comparison.
func (a accountWithLoad) effectivePriority() int {
	p := 0
	if a.account != nil {
		p = a.account.Priority
	}
	return p + a.saturationPenalty
}
