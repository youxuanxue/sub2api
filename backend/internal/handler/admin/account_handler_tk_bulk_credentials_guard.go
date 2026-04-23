package admin

import (
	"context"
	"fmt"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// tkRejectBulkCredentialsForNewAPI returns a 400-level error when any of the
// target accounts is on PlatformNewAPI (Bug B-4).
//
// Rationale: BulkUpdateAccounts persists credentials directly via
// accountRepo.BulkUpdate, which bypasses resolveNewAPIMoonshotBaseURLOnSave
// (the per-save Moonshot regional probe wired into CreateAccount /
// UpdateAccount). A batch api_key swap on Moonshot accounts would persist
// with a wrong base_url (api.moonshot.cn vs .ai), and the relay hot path
// deliberately does NOT do per-request region fallback — every relay
// request would 401 until the operator noticed.
//
// The explicit reject pushes the operator to per-account edits, which DO
// run the resolver. Trade-off: batch convenience is lost for newapi/
// Moonshot, but data integrity is preserved.
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-4 for
// alternative designs considered (per-account in-line resolve was rejected
// because it inflates BulkUpdate to a 25s × N timeout fan-out).
func (h *AccountHandler) tkRejectBulkCredentialsForNewAPI(ctx context.Context, accountIDs []int64) error {
	if h == nil || h.adminService == nil || len(accountIDs) == 0 {
		return nil
	}
	accounts, err := h.adminService.GetAccountsByIDs(ctx, accountIDs)
	if err != nil {
		return err
	}
	for _, acc := range accounts {
		if acc != nil && acc.Platform == service.PlatformNewAPI {
			return infraerrors.BadRequest(
				"BULK_CREDENTIALS_UNSUPPORTED_FOR_NEWAPI",
				fmt.Sprintf(
					"account #%d is on platform=newapi; bulk credentials edits would skip moonshot regional resolve and persist a wrong base_url. Edit each newapi account individually.",
					acc.ID,
				),
			)
		}
	}
	return nil
}
