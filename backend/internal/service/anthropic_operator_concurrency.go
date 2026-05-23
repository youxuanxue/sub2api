package service

import (
	"context"
	"fmt"
	"math"
)

// AnthropicOperatorConcurrencyUserID is the admin/default operator user whose API
// concurrency mirrors Σ concurrency of schedulable=true anthropic rows on this DB,
// aligned with ops/anthropic/manage-anthropic-config.py operator-concurrency SQL.
const AnthropicOperatorConcurrencyUserID int64 = 1

// SyncAnthropicOperatorConcurrency sets users AnthropicOperatorConcurrencyUserID concurrency
// to the sum of non-deleted schedulable=true anthropic account concurrency rows.
func SyncAnthropicOperatorConcurrency(ctx context.Context, accountRepo AccountRepository, userRepo UserRepository) error {
	if accountRepo == nil || userRepo == nil {
		return fmt.Errorf("sync anthropic operator concurrency: nil repository")
	}
	total, err := accountRepo.SumConcurrencyAnthropic(ctx)
	if err != nil {
		return err
	}
	if total < 0 {
		total = 0
	}
	if total > int64(math.MaxInt) {
		return fmt.Errorf("sum anthropic concurrency overflows int (%d)", total)
	}
	_, err = userRepo.BatchSetConcurrency(ctx, []int64{AnthropicOperatorConcurrencyUserID}, int(total))
	return err
}
