package service

import (
	"context"
	"fmt"
	"math"
)

// AnthropicOperatorConcurrencyUserID is the admin/default operator user whose API
// concurrency mirrors Σ concurrency of all anthropic rows on this DB (OAuth + apikey etc.),
// aligned with ops/anthropic/manage-anthropic-config.py edge apply SQL.
const AnthropicOperatorConcurrencyUserID int64 = 1

// SyncAnthropicOperatorConcurrency sets users AnthropicOperatorConcurrencyUserID concurrency
// to the sum of non-deleted anthropic account concurrency rows (all account types).
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
