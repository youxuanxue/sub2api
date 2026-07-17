package service

import (
	"context"
	"time"
)

// TK: pre-flight balance HOLD — the concurrent-overdraft fix (see tk_026).
//
// Balance is deducted AFTER the upstream serves a request (post-hoc) with no
// `balance >= amount` floor, so N concurrent requests from a barely-positive
// balance all pass admission, all get served, then all deduct — driving the
// balance arbitrarily negative. Post-hoc anything cannot un-serve an
// already-served request; the fix is to RESERVE an estimate of the cost BEFORE
// forwarding and RELEASE it when the request ends. Actual billing stays
// untouched on the async path.
//
// Invariant: reserve is atomic `UPDATE users SET balance = balance - hold
// WHERE balance >= hold`. Row-lock serialization ⇒ after admitting k concurrent
// requests balance = B - Σholdᵢ ≥ 0, so Σholdᵢ ≤ B. For explicit request
// ceilings the hold can be a true upper bound and the old non-negative proof
// applies. For omitted token output ceilings the gateway intentionally reserves
// a low default instead: this still collapses concurrent overdraft
// amplification, but it is not a mathematical guarantee that final balance can
// never go below zero. Settlement still bills exact actual.
//
// Release ordering is part of the invariant: billing settles asynchronously,
// so a hold refunded at handler return while its bill is still queued would
// re-expose the funds to concurrent admission. Hence the hand-off protocol —
// when a usage-record task is submitted, the handler hands refund ownership to
// settlement (UsageBillingCommand.TkHoldRequestID) and the hold is consumed in
// the SAME transaction as the balance deduction; only never-billed paths
// refund at handler return, and crashes are repaid by the hold reconciler.
//
// Capability is consumed by type assertion (same pattern as
// UsageBillingVideoRefundApplier) so the wide UsageBillingRepository interface
// and its test stubs stay untouched.

// HoldCommand is the money-movement half of a pre-flight reservation, applied
// at-most-once per request_id by UsageBillingHoldApplier.
type HoldCommand struct {
	RequestID string  // usage-billing request id — reserve/release idempotency key
	UserID    int64   // balance owner (subscription users do not reserve — see hold injection)
	APIKeyID  int64   // recorded for audit/reconciliation only
	Amount    float64 // USD reserved (> 0); released verbatim at request end
}

// UsageBillingHoldApplier is the optional narrow capability the hold path needs
// from the usage-billing repository.
type UsageBillingHoldApplier interface {
	// ReserveBalanceHold atomically deducts cmd.Amount from the user balance
	// guarded by `balance >= amount` and records a usage_holds row, in one
	// transaction. Returns (true, nil) when reserved (or a hold for this
	// request_id already exists — idempotent), (false, nil) when the balance is
	// insufficient (caller rejects with 403), (false, err) on a DB error.
	ReserveBalanceHold(ctx context.Context, cmd *HoldCommand) (reserved bool, err error)

	// ReleaseBalanceHold refunds the reserved amount and deletes the hold row,
	// in one transaction. Idempotent: a missing row returns (false, nil).
	ReleaseBalanceHold(ctx context.Context, requestID string) (released bool, err error)

	// ReleaseExpiredBalanceHolds refunds holds older than olderThan (up to batch
	// rows) — the crash-recovery sweep for reserve/release pairs whose
	// request-end release never ran (process crash mid-request). Returns the
	// number of holds refunded.
	ReleaseExpiredBalanceHolds(ctx context.Context, olderThan time.Time, batch int) (refunded int, err error)
}
