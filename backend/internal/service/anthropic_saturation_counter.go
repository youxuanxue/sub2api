package service

import "context"

// AnthropicSaturationCounterCache tracks, per anthropic mirror-stub account, a
// short-window count of recent *downstream-capacity* hits — i.e. responses where
// tkSkipDownstreamNoAvailableAccountsPenalty / tkSkipDownstreamFailoverExhaustedPenalty
// fired (the forwarded-to edge pool was empty or its failover loop ran dry). The
// stub itself is healthy; this counter is NOT a cooldown and NEVER advances the
// 3/3 ladder or SetTempUnschedulable. It is read by the load-aware account
// scheduler to apply a BOUNDED de-prioritization preference so prod stops
// selecting a saturated edge stub first and wasting a failover hop on every
// request for the whole ~47-min upstream-limit window.
//
// Self-clearing by construction: the counter is a fixed window with a short TTL,
// and the scheduler reads it live on every selection. When the edge recovers,
// the no-available hits stop, the counter expires, and the preference evaporates
// with no separate clear-on-200 hook, marker, or cooldown state.
type AnthropicSaturationCounterCache interface {
	// IncrementSaturation records one downstream-capacity hit for accountID. The
	// fixed-window count has TTL=windowSeconds (set only when an empty key is
	// first INCR'd, so a sustained burst keeps the original window rather than
	// sliding it forward indefinitely). Returns the new fixed-window count.
	//
	// The SAME call also maintains a sliding "streak" pair (firstSeen + lastSeen
	// epochs, sliding TTL refreshed on every hit) used by the sustained-saturation
	// HARD exclusion (GetSaturationStreakBatch). The fixed count drives the bounded
	// soft preference; the streak span drives the hard floor. Both are fed from the
	// single increment site (recordAnthropicStubSaturation), so they stay in sync.
	IncrementSaturation(ctx context.Context, accountID int64, windowSeconds int) (count int64, err error)

	// GetSaturationBatch returns the current in-window counts for accountIDs in a
	// single round trip (MGET). Missing/expired keys map to 0. The scheduler
	// scores a whole candidate set per selection, so a batch read avoids N
	// sequential Redis calls on the hot path.
	GetSaturationBatch(ctx context.Context, accountIDs []int64) (map[int64]int64, error)

	// GetSaturationStreakBatch returns the per-account sliding-streak state
	// (firstSeen/lastSeen epochs) for accountIDs in one round trip. Missing/expired
	// keys are absent from the map. Used by the sustained-saturation HARD exclusion
	// (tkFilterSustainedlySaturated): a stub whose continuous-saturation streak has
	// spanned >= a minimum age is dead long enough to route around entirely, not
	// just de-prioritize. The streak keys self-clear via their sliding TTL once the
	// edge recovers and hits stop.
	GetSaturationStreakBatch(ctx context.Context, accountIDs []int64) (map[int64]AnthropicSaturationStreak, error)
}

// AnthropicSaturationStreak is the sliding continuous-saturation streak state for
// one mirror stub. FirstSeenUnix is the epoch of the first hit of the current
// streak (set once, refreshed-TTL each hit); LastSeenUnix is the epoch of the most
// recent hit. The streak duration LastSeenUnix-FirstSeenUnix is the sustained
// signal: it can only reach the minimum age via continuous hits over real wall
// time, so a transient burst (span ~seconds) can never satisfy it. Both zero =>
// no active streak (keys absent/expired).
type AnthropicSaturationStreak struct {
	FirstSeenUnix int64
	LastSeenUnix  int64
}
