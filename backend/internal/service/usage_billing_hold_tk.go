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
// already-served request; the only fix is to RESERVE an upper-bound estimate
// of the cost BEFORE forwarding and RELEASE it when the request ends. Actual
// billing stays untouched on the async path.
//
// Invariant: reserve is atomic `UPDATE users SET balance = balance - hold
// WHERE balance >= hold`. Row-lock serialization ⇒ after admitting k concurrent
// requests balance = B - Σholdᵢ ≥ 0, so Σholdᵢ ≤ B. At request end each releases
// its hold; the async path bills actualᵢ. If hold is a true upper bound
// (actualᵢ ≤ holdᵢ — guaranteed by the EstimateHold* formulas in
// billing_service_tk_hold.go), then final balance = B - Σactualᵢ ≥ B - Σholdᵢ ≥ 0.
// Provably never negative. The hold's deliberate over-estimate only briefly
// shrinks AVAILABLE balance; settlement still bills exact actual.
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
	// insufficient (caller rejects with 402), (false, err) on a DB error.
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
