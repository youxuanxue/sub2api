package service

import "context"

// AntigravitySaturationCounterCache tracks recent downstream-capacity hits for
// prod Antigravity edge-relay stubs. Reads and writes share the resolved model
// scope and fixed window; saturation is a preference, never a cooldown.
type AntigravitySaturationCounterCache interface {
	IncrementSaturation(ctx context.Context, accountID int64, modelKey string, windowSeconds int) (count int64, err error)
}

type AntigravitySaturationScope struct {
	AccountID int64
	ModelKey  string
}

type AntigravitySaturationReader interface {
	GetSaturationBatch(context.Context, []AntigravitySaturationScope) (map[AntigravitySaturationScope]int64, error)
}
