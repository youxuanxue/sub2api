package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TK: pre-flight balance HOLD repository methods — money movement, atomic.
// Implements service.UsageBillingHoldApplier (consumed via type assertion; the
// wide UsageBillingRepository interface stays Apply-only). See tk_026 and
// service/usage_billing_hold_tk.go for the why and the invariant.

// ReserveBalanceHold atomically records a usage_holds row AND deducts the
// reserved amount from the user balance, guarded by `balance >= amount`.
//
// Both effects share one transaction, so the outcome is all-or-nothing:
//   - hold row inserted + balance deducted  → reserved (true)
//   - balance insufficient (deduct matches 0 rows) → both rolled back (false)
//   - hold row already exists for this request_id  → idempotent no-op (true);
//     never double-deducts (a retried reserve for the same request must not
//     charge twice).
func (r *usageBillingRepository) ReserveBalanceHold(ctx context.Context, cmd *service.HoldCommand) (bool, error) {
	if cmd == nil || cmd.Amount <= 0 {
		return false, nil
	}
	if r == nil || r.db == nil {
		return false, errors.New("usage billing repository db is nil")
	}
	if cmd.RequestID == "" {
		return false, service.ErrUsageBillingRequestIDRequired
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

	// Claim the hold row first. ON CONFLICT DO NOTHING makes a retried reserve
	// for the same request_id idempotent: no new row → already reserved → do
	// NOT deduct again.
	var insertedID string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO usage_holds (request_id, user_id, api_key_id, amount)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (request_id) DO NOTHING
		RETURNING request_id
	`, cmd.RequestID, cmd.UserID, cmd.APIKeyID, cmd.Amount).Scan(&insertedID)
	if errors.Is(err, sql.ErrNoRows) {
		// Hold already exists for this request — reservation stands, money was
		// already moved by the first reserve. Commit (no-op) and report held.
		if err := tx.Commit(); err != nil {
			return false, err
		}
		tx = nil
		return true, nil
	}
	if err != nil {
		return false, err
	}

	// Atomic floor-guarded deduction. 0 rows ⇒ balance < amount ⇒ reject; the
	// hold INSERT rolls back with it, so no orphan hold is left behind.
	var newBalance float64
	err = tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance - $1,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL AND balance >= $1
		RETURNING balance
	`, cmd.Amount, cmd.UserID).Scan(&newBalance)
	if errors.Is(err, sql.ErrNoRows) {
		// Insufficient balance (or user missing): roll back the hold INSERT.
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	tx = nil
	return true, nil
}

// ReleaseBalanceHold refunds the reserved amount and removes the hold row, in
// one transaction. Idempotent: a missing row (already released, or refunded by
// the reconciler) returns (false, nil) and moves no money.
func (r *usageBillingRepository) ReleaseBalanceHold(ctx context.Context, requestID string) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("usage billing repository db is nil")
	}
	if requestID == "" {
		return false, nil
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

	var userID int64
	var amount float64
	err = tx.QueryRowContext(ctx, `
		DELETE FROM usage_holds
		WHERE request_id = $1
		RETURNING user_id, amount
	`, requestID).Scan(&userID, &amount)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing held for this request — already released or never reserved.
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE users
		SET balance = balance + $1,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`, amount, userID); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	tx = nil
	return true, nil
}

// ReleaseExpiredBalanceHolds refunds holds older than olderThan (up to batch
// rows) in a single transaction — the crash-recovery sweep for reserve/release
// pairs whose request-end release never ran. FOR UPDATE SKIP LOCKED keeps it
// from fighting concurrent normal releases; the per-user SUM folds multiple
// leaked holds for one user into one balance update.
func (r *usageBillingRepository) ReleaseExpiredBalanceHolds(ctx context.Context, olderThan time.Time, batch int) (int, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("usage billing repository db is nil")
	}
	if batch <= 0 {
		batch = 500
	}

	var refunded int
	err := r.db.QueryRowContext(ctx, `
		WITH picked AS (
			SELECT request_id
			FROM usage_holds
			WHERE created_at < $1
			ORDER BY created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		),
		deleted AS (
			DELETE FROM usage_holds
			WHERE request_id IN (SELECT request_id FROM picked)
			RETURNING user_id, amount
		),
		agg AS (
			SELECT user_id, SUM(amount) AS total
			FROM deleted
			GROUP BY user_id
		),
		upd AS (
			UPDATE users u
			SET balance = balance + agg.total,
				updated_at = NOW()
			FROM agg
			WHERE u.id = agg.user_id AND u.deleted_at IS NULL
			RETURNING 1
		)
		SELECT COUNT(*) FROM deleted
	`, olderThan, batch).Scan(&refunded)
	if err != nil {
		return 0, err
	}
	return refunded, nil
}
