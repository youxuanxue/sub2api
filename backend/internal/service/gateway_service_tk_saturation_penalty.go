package service

import (
	"context"
	"log/slog"
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

// computeAnthropicSaturationPenalties fills accountWithLoad.saturationPenalty for
// each candidate when the feature is enabled. The penalty is non-zero ONLY for
// anthropic accounts whose live in-window saturation count is at/above
// anthropicSaturationThreshold (transient blips below threshold are untouched, so
// current behaviour is preserved). Best-effort: nil receiver/cache, disabled
// kill-switch, or any Redis error leaves all penalties at 0 (pre-feature
// scoring). Logs at most once per selection (the set of newly-penalized IDs).
func (s *GatewayService) computeAnthropicSaturationPenalties(ctx context.Context, candidates []accountWithLoad) {
	if s == nil || s.tkAnthropicSaturationCounter == nil || len(candidates) == 0 {
		return
	}
	if s.settingService != nil && !s.settingService.IsAnthropicSaturatedStubDeprioritizeEnabled(ctx) {
		return
	}

	ids := make([]int64, 0, len(candidates))
	for i := range candidates {
		acc := candidates[i].account
		if acc != nil && acc.Platform == PlatformAnthropic {
			ids = append(ids, acc.ID)
		}
	}
	if len(ids) == 0 {
		return
	}

	counts, err := s.tkAnthropicSaturationCounter.GetSaturationBatch(ctx, ids)
	if err != nil {
		slog.Warn("anthropic_saturation_penalty_read_failed", "error", err)
		return
	}
	if len(counts) == 0 {
		return
	}

	var penalized []int64
	for i := range candidates {
		acc := candidates[i].account
		if acc == nil || acc.Platform != PlatformAnthropic {
			continue
		}
		if counts[acc.ID] >= anthropicSaturationThreshold {
			candidates[i].saturationPenalty = anthropicSaturationPriorityPenalty
			penalized = append(penalized, acc.ID)
		}
	}
	if len(penalized) > 0 {
		slog.Debug("anthropic_saturation_penalty_applied",
			"account_ids", penalized,
			"penalty", anthropicSaturationPriorityPenalty,
			"candidate_count", len(candidates))
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
