package service

import (
	"context"
	"fmt"
	"math"
)

// AnthropicOAuthOperatorConcurrencyUserID is the admin/default operator user whose
// API concurrency mirrors Σ anthropic OAuth account pool capacity on this DB,
// aligned with ops/anthropic/manage-anthropic-config.py edge apply SQL.
const AnthropicOAuthOperatorConcurrencyUserID int64 = 1

// SyncAnthropicOAuthOperatorConcurrency sets users AnthropicOAuthOperatorConcurrencyUserID
// concurrency to the sum of non-deleted Anthropic oauth account concurrency rows.
func SyncAnthropicOAuthOperatorConcurrency(ctx context.Context, accountRepo AccountRepository, userRepo UserRepository) error {
	if accountRepo == nil || userRepo == nil {
		return fmt.Errorf("sync anthropic oauth operator concurrency: nil repository")
	}
	total, err := accountRepo.SumConcurrencyAnthropicOAuth(ctx)
	if err != nil {
		return err
	}
	if total < 0 {
		total = 0
	}
	if total > int64(math.MaxInt) {
		return fmt.Errorf("sum anthropic oauth concurrency overflows int (%d)", total)
	}
	_, err = userRepo.BatchSetConcurrency(ctx, []int64{AnthropicOAuthOperatorConcurrencyUserID}, int(total))
	return err
}
