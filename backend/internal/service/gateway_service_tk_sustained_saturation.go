package service

import (
	"context"
	"log/slog"
)

// TK — anthropic SUSTAINEDLY-saturated mirror-stub HARD exclusion.
//
// Problem (prod, recurring): prod reaches Anthropic only through per-edge api-key
// mirror "stub" accounts (cc-<edge>) that forward to one Edge gateway. When an
// edge's sole OAuth account is window-rate-limited (~30m+), the edge returns
// TokenKey's own header-less capacity envelope ("No available accounts" 429 /
// "Upstream rate limit exceeded" 502). The skip predicates
// (tkSkipDownstreamNoAvailableAccountsPenalty / tkIsAnthropicNonAuthoritative429)
// correctly suppress cooldown + fail over — but DELIBERATELY keep the stub fully
// schedulable (the 2026-05-31 "503 amplifier" fix: cooling stubs on a transient
// edge blip collapses the whole pool). The consequence for a stub whose edge is
// dead for a SUSTAINED window: the stub is never marked unschedulable, ties its
// healthy siblings on base priority, and — because LRU favours the perpetually-
// failing stub (its LastUsedAt only advances on SUCCESS) — keeps getting picked
// FIRST every request, wasting a failover hop on the dead edge and (when that hop
// + edge latency exceeds the client's timeout) starving healthy siblings while
// the client retries. Operator-disabling the stub fixes it instantly because that
// is a HARD exclusion (Schedulable=false) — the exact primitive the existing
// bounded soft de-prioritization (gateway_service_tk_saturation_penalty.go) never
// performs.
//
// This file adds the missing primitive: a HARD-but-SHORT, self-clearing exclusion
// for a stub that has been CONTINUOUSLY saturated for at least a minimum age. It
// is the hard tier above the soft +1000 preference (which stays for the moderate /
// young-saturation case), and it is amplifier-safe BY CONSTRUCTION:
//
//   - Distinguishes "edge dead 30m" from "edge transiently dry 5s" via the streak
//     SPAN (lastSeen-firstSeen), which can only reach the min-age via continuous
//     hits over real wall time. A 3-request burst (span ~seconds) can NEVER trip
//     it, so it cannot reproduce the 503-amplifier collapse.
//   - Span-based, not rate-based: robust at LOW traffic (a few hits/min sustained
//     for >= min-age still trips it), where the fixed-window count never could.
//   - Never empties the pool: the all-saturated last-resort guard keeps every
//     candidate when exclusion would leave nothing schedulable.
//   - Self-clearing: recomputed live each selection from the short sliding-TTL
//     streak keys; once the edge recovers and hits stop, the keys expire and the
//     stub is re-included with no cooldown state, no SetTempUnschedulable, no
//     ladder advance.
//   - Request-owned policy 429s never feed the counter (recordAnthropicStubSaturation
//     is only reached on the downstream-capacity skip branches), so the same-text /
//     request-owned breaker is untouched.
//
// Shares the kill-switch (SettingKeyAnthropicSaturatedStubDeprioritizeEnabled) and
// the Redis counter (tkAnthropicSaturationCounter) with the soft preference; when
// either is unset the whole feature is inert.

// anthropicSustainedSaturationMinAgeSeconds is the minimum continuous-saturation
// streak SPAN (lastSeen-firstSeen) at which a mirror stub is treated as
// sustainedly dead and HARD-excluded from selection. A span this large requires
// capacity-envelope hits spanning >= this many seconds within the sliding streak
// window, which a transient blip / request burst cannot produce. MUST stay <
// anthropicSaturationStreakTTLSeconds (repository layer) so the streak keys do not
// expire before the span can accumulate.
const anthropicSustainedSaturationMinAgeSeconds int64 = 120

// tkIsAnthropicStubSustainedlySaturated reports whether a streak state represents
// a sustained outage: a real, non-zero streak whose span has reached the min age.
// Pure; no I/O, no clock — the span is computed entirely from the stored epochs
// (lastSeen is the most recent hit), so it is deterministic and trivially
// testable. A span >= min-age implies >= 2 hits spanning that duration, so a
// single stray hit (firstSeen==lastSeen => span 0) never qualifies.
func tkIsAnthropicStubSustainedlySaturated(st AnthropicSaturationStreak) bool {
	if st.FirstSeenUnix <= 0 || st.LastSeenUnix <= 0 {
		return false
	}
	return st.LastSeenUnix-st.FirstSeenUnix >= anthropicSustainedSaturationMinAgeSeconds
}

// tkFilterSustainedlySaturated removes sustainedly-saturated anthropic mirror
// stubs from a selection candidate set, with an all-saturated last-resort guard.
// Returns the input unchanged when the feature is off / inert, when there is < 2
// candidates (cannot drop without emptying), or when applying the exclusion would
// leave nothing schedulable (all candidates saturated). Best-effort: any Redis
// error leaves the candidate set untouched — selection must never fail because the
// streak counter is unavailable.
//
// Called on the Layer-1 (model-routing) and Layer-2 (load-aware) candidate sets so
// the exclusion covers BOTH paths — unlike the soft preference, which the routing
// path never applies.
func (s *GatewayService) tkFilterSustainedlySaturated(ctx context.Context, candidates []*Account) []*Account {
	if s == nil || s.tkAnthropicSaturationCounter == nil || len(candidates) < 2 {
		return candidates
	}
	if s.settingService != nil && !s.settingService.IsAnthropicSaturatedStubDeprioritizeEnabled(ctx) {
		return candidates
	}

	ids := make([]int64, 0, len(candidates))
	for _, acc := range candidates {
		if acc != nil && acc.Platform == PlatformAnthropic {
			ids = append(ids, acc.ID)
		}
	}
	if len(ids) == 0 {
		return candidates
	}

	states, err := s.tkAnthropicSaturationCounter.GetSaturationStreakBatch(ctx, ids)
	if err != nil {
		slog.Warn("anthropic_sustained_saturation_read_failed", "error", err)
		return candidates
	}
	if len(states) == 0 {
		return candidates
	}

	kept := make([]*Account, 0, len(candidates))
	var dropped []int64
	for _, acc := range candidates {
		if acc != nil && acc.Platform == PlatformAnthropic {
			if st, ok := states[acc.ID]; ok && tkIsAnthropicStubSustainedlySaturated(st) {
				dropped = append(dropped, acc.ID)
				continue
			}
		}
		kept = append(kept, acc)
	}

	// All-saturated last-resort guard: never empty the pool. If nothing was
	// dropped, or dropping would leave zero candidates, keep the original set
	// (the bounded soft preference still de-prioritizes them relative to any
	// healthier stub).
	if len(dropped) == 0 || len(kept) == 0 {
		return candidates
	}

	slog.Info("anthropic_sustained_saturation_excluded",
		"excluded_account_ids", dropped,
		"kept", len(kept),
		"candidates", len(candidates),
		"min_age_seconds", anthropicSustainedSaturationMinAgeSeconds)
	return kept
}
