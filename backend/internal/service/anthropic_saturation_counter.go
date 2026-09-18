package service

import "context"

// AnthropicSaturationCounterCache tracks, per anthropic mirror-stub account, a
// rolling-window count of recent *downstream-capacity* hits — i.e. responses where
// tkSkipDownstreamNoAvailableAccountsPenalty / tkSkipDownstreamFailoverExhaustedPenalty
// fired (the forwarded-to edge pool was empty or its failover loop ran dry). The
// stub itself is healthy; this counter is NOT a cooldown and NEVER advances the
// anthropic_upstream_error ladder or SetTempUnschedulable. It is read by the
// load-aware account scheduler to apply a soft, count-growing de-prioritization
// preference (see edge_mirror_stub_saturation_tk.go) so prod stops selecting a
// saturated edge stub first and wasting a failover hop on every request for the
// whole ~47-min upstream-limit window.
//
// Self-clearing by construction: individual events leave the rolling window, and
// the scheduler reads the live count on every selection. When the edge recovers,
// the no-available hits stop, events expire, and the preference evaporates with
// no separate clear-on-200 hook, marker, or cooldown state.
type AnthropicSaturationCounterCache interface {
	// IncrementSaturation records one downstream-capacity hit for accountID.
	// Individual events expire after windowSeconds; sustained bursts keep
	// accumulating instead of resetting to a fixed-window count.
	IncrementSaturation(ctx context.Context, accountID int64, windowSeconds int) (count int64, err error)

	// GetSaturationBatch returns the current in-window counts for accountIDs in a
	// single round trip (ZCOUNT). Missing/expired keys map to 0. The scheduler
	// scores a whole candidate set per selection, so a batch read avoids N
	// sequential Redis calls on the hot path.
	GetSaturationBatch(ctx context.Context, accountIDs []int64, windowSeconds int) (map[int64]int64, error)
}
