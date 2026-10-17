package service

import "context"

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
