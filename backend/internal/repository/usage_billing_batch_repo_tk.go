package repository

import (
	"context"
	"database/sql"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Held commands use the existing settlement owner in command order within the
// batch transaction. Refunding every hold before all deductions would change
// each request's reported balance/overdraft result and expose reserved funds.
func (r *usageBillingRepository) tkApplyHeldBillingBatch(ctx context.Context, tx *sql.Tx, cmds []*service.UsageBillingCommand, applied []bool, outcomes []service.UsageBillingBatchOutcome) (bool, error) {
	held := false
	for _, cmd := range cmds {
		if cmd.TkHoldRequestID != "" {
			held = true
			break
		}
	}
	if !held {
		return false, nil
	}
	for i, cmd := range cmds {
		if applied[i] {
			if err := r.applyUsageBillingEffects(ctx, tx, cmd, outcomes[i].Result); err != nil {
				return true, err
			}
		}
	}
	return true, nil
}
