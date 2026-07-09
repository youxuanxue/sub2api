package repository

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TK: video terminal-failure refund — money movement, applied at most once.
// Implements service.UsageBillingVideoRefundApplier (consumed via type
// assertion; the wide UsageBillingRepository interface stays Apply-only).
//
// Idempotency rides the same usage_billing_dedup table as forward billing:
// the refund claims "video-refund:<public_task_id>" before touching money,
// so concurrent terminal polls (or a retried fetch) refund exactly once.
//
// Reversal scope mirrors the forward effects with two deliberate gaps:
//   - rate-limit windows (usage_5h/1d/7d) are NOT decremented — they are
//     rolling time windows; crediting a window after the fact would let a
//     burst of failures unlock extra throughput retroactively.
//   - upstream account quota (accounts.extra quota_used) is NOT decremented —
//     the upstream did run (and fail) the task; whether the upstream charges
//     for failures is its contract, not the user's.
func (r *usageBillingRepository) ApplyVideoRefund(ctx context.Context, cmd *service.VideoRefundCommand) (bool, error) {
	if cmd == nil || cmd.Amount <= 0 {
		return false, nil
	}
	if r == nil || r.db == nil {
		return false, errors.New("usage billing repository db is nil")
	}
	if cmd.RequestID == "" {
		return false, domain.ErrUsageBillingRequestIDRequired
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	// Reuse the forward-billing claim (same conflict semantics): build a
	// minimal command so the fingerprint is deterministic for retries.
	claimCmd := &domain.UsageBillingCommand{
		RequestID:   cmd.RequestID,
		APIKeyID:    cmd.APIKeyID,
		UserID:      cmd.UserID,
		BillingType: cmd.BillingType,
		BalanceCost: -cmd.Amount,
	}
	claimCmd.Normalize()
	claimed, err := r.claimUsageBillingKey(ctx, tx, claimCmd)
	if err != nil {
		return false, err
	}
	if !claimed {
		return false, nil
	}

	if cmd.BillingType == service.BillingTypeSubscription && cmd.SubscriptionID != nil {
		// Floor at 0: if a usage window rolled over between charge and
		// refund, the counter may already be below the refund amount.
		if _, err := tx.ExecContext(ctx, `
			UPDATE user_subscriptions
			SET daily_usage_usd = GREATEST(daily_usage_usd - $1, 0),
				weekly_usage_usd = GREATEST(weekly_usage_usd - $1, 0),
				monthly_usage_usd = GREATEST(monthly_usage_usd - $1, 0),
				updated_at = NOW()
			WHERE id = $2 AND deleted_at IS NULL
		`, cmd.Amount, *cmd.SubscriptionID); err != nil {
			return false, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE users
			SET balance = balance + $1,
				updated_at = NOW()
			WHERE id = $2 AND deleted_at IS NULL
		`, cmd.Amount, cmd.UserID); err != nil {
			return false, err
		}
	}

	// Release api-key quota symmetric to incrementUsageBillingAPIKeyQuota:
	// floor at 0, and re-activate a key the original charge exhausted when
	// the released usage drops back below the quota. WHERE quota > 0 keeps
	// this a no-op for unlimited keys (mirrors shouldDeductAPIKeyQuota).
	if _, err := tx.ExecContext(ctx, `
		UPDATE api_keys
		SET quota_used = GREATEST(quota_used - $1, 0),
			status = CASE
				WHEN status = $3
					AND GREATEST(quota_used - $1, 0) < quota
				THEN $4
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL AND quota > 0
	`, cmd.Amount, cmd.APIKeyID, service.StatusAPIKeyQuotaExhausted, service.StatusAPIKeyActive); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	tx = nil
	return true, nil
}
