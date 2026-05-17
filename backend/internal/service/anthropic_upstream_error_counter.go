package service

import "context"

type AnthropicUpstreamErrorCounterCache interface {
	IncrementAnthropicUpstreamErrorCount(ctx context.Context, accountID int64, windowMinutes int) (int64, error)
	ResetAnthropicUpstreamErrorCount(ctx context.Context, accountID int64) error
}
