package service

import (
	"context"
)

// TK — anthropic saturated mirror-stub de-prioritization (score side).
//
// See ratelimit_service_tk_saturation.go for the increment side and the prod
// problem statement. This file is the READ side: a BOUNDED preference term added
// to the existing load-aware candidate ranking so prod's account selection
// routes AWAY from an anthropic stub that is emitting SUSTAINED downstream-
// capacity 429/502 ("No available accounts" / "all available accounts
// exhausted"). It is a routing PREFERENCE, NOT a cooldown:
//
//   - de-prioritize  — a saturated stub's effective priority is bumped into a
//     worse bucket (filterByMinPriority picks the smallest), so it sorts AFTER
//     any non-saturated stub.
//   - last-resort / never-unschedulable — the penalty is a bounded additive
//     constant; the stub stays in the candidate set (and the Layer-3 fallback
//     queue), so if it is the only/highest candidate it is still selected. The
//     feature NEVER calls SetTempUnschedulable / SetRateLimited / advances the
//     3/3 ladder.
//   - self-clearing — the penalty is recomputed per selection from the LIVE
//     Redis count, which has a short TTL; when the edge recovers, the count
//     expires and the preference evaporates with no clear-on-200 hook.
//   - all-saturated safety — if every candidate is saturated, all get the SAME
//     additive penalty, so their RELATIVE order is preserved and selection still
//     returns a stub. Safe by construction: the penalty is a bounded ADD, never
//     a sentinel/exclusion value.

// Penalty magnitude: edge_mirror_stub_saturation_tk.go (SSOT).

// SetAnthropicSaturationCounter wires the Redis-backed saturation counter into
// GatewayService post-construction (mirrors SetAnthropicSigPreemptCache). Nil-
// safe: when unset, computeAnthropicSaturationPenalties is a no-op and selection
// is identical to pre-feature behaviour.
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
