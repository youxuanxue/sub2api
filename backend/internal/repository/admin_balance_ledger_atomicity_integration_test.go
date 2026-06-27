//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const integrationLedgerFailNote = "__TK_INTEGRATION_LEDGER_FAIL__"

func installLedgerFailTrigger(t *testing.T) {
	t.Helper()
	_, err := integrationDB.Exec(`
CREATE OR REPLACE FUNCTION tk_test_fail_balance_ledger() RETURNS trigger AS $$
BEGIN
  IF NEW.notes = '` + integrationLedgerFailNote + `' THEN
    RAISE EXCEPTION 'integration: forced ledger insert failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS tk_test_fail_balance_ledger_trg ON redeem_codes;
CREATE TRIGGER tk_test_fail_balance_ledger_trg
  BEFORE INSERT ON redeem_codes
  FOR EACH ROW EXECUTE FUNCTION tk_test_fail_balance_ledger();
`)
	require.NoError(t, err, "install ledger fail trigger")
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DROP TRIGGER IF EXISTS tk_test_fail_balance_ledger_trg ON redeem_codes`)
		_, _ = integrationDB.Exec(`DROP FUNCTION IF EXISTS tk_test_fail_balance_ledger()`)
	})
}

func newAdminServiceForBalanceLedgerTests(t *testing.T) (service.AdminService, service.UserRepository) {
	t.Helper()
	client := testEntClient(t)
	userRepo := NewUserRepository(client, integrationDB)
	redeemRepo := NewRedeemCodeRepository(client)
	adminSvc := service.NewAdminService(
		userRepo,
		nil, nil, nil, nil,
		redeemRepo,
		nil, nil, nil, nil, nil, nil,
		client,
		nil, nil, nil, nil, nil, nil,
	)
	return adminSvc, userRepo
}

// TestAdminService_UpdateUserBalance_RollsBackOnLedgerFailure verifies Tier1:
// when the redeem_codes journal insert fails inside persistBalanceAdjustment's
// ent transaction, users.balance must not commit the adjustment.
func TestAdminService_UpdateUserBalance_RollsBackOnLedgerFailure(t *testing.T) {
	installLedgerFailTrigger(t)

	ctx := context.Background()
	adminSvc, userRepo := newAdminServiceForBalanceLedgerTests(t)
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{Balance: 100})
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM redeem_codes WHERE used_by = $1`, user.ID)
		_, _ = integrationDB.Exec(`DELETE FROM users WHERE id = $1`, user.ID)
	})

	_, err := adminSvc.UpdateUserBalance(ctx, user.ID, 50, "add", integrationLedgerFailNote)
	require.Error(t, err, "ledger failure must surface to caller")

	got, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100.0, got.Balance, 0.0001, "balance must roll back when journal insert fails")

	var redeemCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM redeem_codes WHERE used_by = $1`, user.ID).Scan(&redeemCount))
	require.Zero(t, redeemCount, "no journal row must escape a rolled-back adjustment")
}

func TestAdminService_UpdateUserBalance_CommitsBalanceAndLedgerAtomically(t *testing.T) {
	ctx := context.Background()
	adminSvc, userRepo := newAdminServiceForBalanceLedgerTests(t)
	client := testEntClient(t)

	user := mustCreateUser(t, client, &service.User{Balance: 100})
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM redeem_codes WHERE used_by = $1`, user.ID)
		_, _ = integrationDB.Exec(`DELETE FROM users WHERE id = $1`, user.ID)
	})

	updated, err := adminSvc.UpdateUserBalance(ctx, user.ID, 50, "add", "integration atomic ok")
	require.NoError(t, err)
	require.InDelta(t, 150.0, updated.Balance, 0.0001)

	got, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 150.0, got.Balance, 0.0001)

	var redeemCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM redeem_codes WHERE used_by = $1 AND notes = $2`, user.ID, "integration atomic ok").Scan(&redeemCount))
	require.Equal(t, 1, redeemCount)
}
