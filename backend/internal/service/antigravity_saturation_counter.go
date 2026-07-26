package service

import "context"

// AntigravitySaturationCounterCache tracks recent downstream-capacity hits for
// prod Antigravity edge-relay stubs. It is write-only from RateLimitService;
// scheduling observes the resulting exact-model cooldown instead of an
// account-wide saturation preference.
type AntigravitySaturationCounterCache interface {
	IncrementSaturation(ctx context.Context, accountID int64, modelKey string, windowSeconds int) (count int64, err error)
}
