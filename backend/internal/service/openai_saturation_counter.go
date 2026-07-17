package service

import "context"

// OpenAISaturationCounterCache tracks, per OpenAI edge-mirror stub account, a
// short-window count of recent downstream-capacity hits (forwarded edge returned
// TokenKey's own "no available accounts" / failover-exhausted envelope). Same
// semantics as AnthropicSaturationCounterCache — a routing preference, not a
// cooldown. See ratelimit_service_tk_openai_saturation.go.
type OpenAISaturationCounterCache interface {
	IncrementSaturation(ctx context.Context, accountID int64, windowSeconds int) (count int64, err error)
	GetSaturationBatch(ctx context.Context, accountIDs []int64) (map[int64]int64, error)
}
