//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
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
		nil, userRepo,
		nil, nil, nil, nil, // group/account/proxy/api-key repos
		redeemRepo,
		nil, nil, nil, nil, nil, nil, // user-group/rpm/billing/proxy/auth helpers
		client,
		nil, nil, nil, nil, nil, nil, // settings/subscription/privacy/runtime/availability helpers
		nil,      // affiliate service
		nil, nil, // composite route repo/resolver
		nil, nil, // channel cache invalidator and account recovery
	)
	return adminSvc, userRepo
}

// TestAdminService_UpdateUserBalance_RollsBackOnLedgerFailure verifies admin
// balance adjustments roll back when the redeem_codes journal insert fails.
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
	require.Error(t, err, "ledger failure must roll back the balance adjustment")

	got, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100.0, got.Balance, 0.0001, "balance must stay unchanged when journal insert fails")
	require.Zero(t, got.TotalRecharged)

	var redeemCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM redeem_codes WHERE used_by = $1`, user.ID).Scan(&redeemCount))
	require.Zero(t, redeemCount, "failed journal insert must not leave a redeem_codes row")
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
	require.Equal(t, 50.0, updated.TotalRecharged)

	got, err := userRepo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	require.InDelta(t, 150.0, got.Balance, 0.0001)

	var redeemCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM redeem_codes WHERE used_by = $1 AND notes = $2`, user.ID, "integration atomic ok").Scan(&redeemCount))
	require.Equal(t, 1, redeemCount)
}

func TestAdminService_CreateUser_ReturnsRechargedOpeningBalance(t *testing.T) {
	ctx := context.Background()
	adminSvc, userRepo := newAdminServiceForBalanceLedgerTests(t)
	balance := 100.0
	created, err := adminSvc.CreateUser(ctx, &service.CreateUserInput{
		Email:    uniqueTestValue(t, "opening") + "@example.com",
		Password: "integration-password", Balance: &balance,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM redeem_codes WHERE used_by = $1`, created.ID)
		_, _ = integrationDB.Exec(`DELETE FROM users WHERE id = $1`, created.ID)
	})
	stored, err := userRepo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, balance, stored.TotalRecharged)
	require.Equal(t, stored.TotalRecharged, created.TotalRecharged)
}

func TestAdminService_BalanceLedger_QualifyingTotals(t *testing.T) {
	ctx := context.Background()
	adminSvc, userRepo := newAdminServiceForBalanceLedgerTests(t)
	client := testEntClient(t)
	cases := []struct {
		name, operation, notes string
		amount, balance, total float64
	}{
		{"add", "add", "operator", 0.5, 1.5, 0.5},
		{"set-up", "set", "operator", 1.5, 1.5, 0.5},
		{"set-down", "set", "operator", 0.5, 0.5, 0},
		{"subtract", "subtract", "operator", 0.5, 0.5, 0},
		{"unchanged", "set", "operator", 1, 1, 0},
		{"verbatim-note", "add", " " + service.BalanceGrantNoteSignup + " ", 0.5, 1.5, 0.5},
	}
	for _, note := range service.GiftBalanceGrantNotes() {
		cases = append(cases, struct {
			name, operation, notes string
			amount, balance, total float64
		}{note, "add", note, 0.5, 1.5, 0})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := mustCreateUser(t, client, &service.User{Balance: 1})
			t.Cleanup(func() {
				_, _ = integrationDB.Exec(`DELETE FROM redeem_codes WHERE used_by = $1`, u.ID)
				_, _ = integrationDB.Exec(`DELETE FROM users WHERE id = $1`, u.ID)
			})
			updated, err := adminSvc.UpdateUserBalance(ctx, u.ID, tc.amount, tc.operation, tc.notes)
			require.NoError(t, err)
			require.Equal(t, tc.balance, updated.Balance)
			require.Equal(t, tc.total, updated.TotalRecharged)
			stored, err := userRepo.GetByID(ctx, u.ID)
			require.NoError(t, err)
			sum, err := NewRedeemCodeRepository(client).SumPositiveBalanceByUser(ctx, u.ID)
			require.NoError(t, err)
			require.Equal(t, stored.TotalRecharged, sum)
			decision := service.EvaluateTrialUnpaidMedia(ctx, stored, service.ShapeOpenAIImages, "POST", nil)
			require.Equal(t, tc.total == 0, decision.Blocked)
		})
	}
}

func TestAdminService_BalanceLedger_RollsBackOnTotalRechargedFailure(t *testing.T) {
	ctx := context.Background()
	adminSvc, userRepo := newAdminServiceForBalanceLedgerTests(t)
	u := mustCreateUser(t, testEntClient(t), &service.User{Balance: 1})
	// Fail after the journal INSERT, at the cumulative total UPDATE.
	_, err := integrationDB.Exec(fmt.Sprintf(`
CREATE FUNCTION tk_test_fail_total_recharged() RETURNS trigger AS $$
BEGIN
  IF NEW.id = %d AND NEW.total_recharged <> OLD.total_recharged THEN
    RAISE EXCEPTION 'integration: forced total update failure';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER tk_test_fail_total_recharged_trg BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION tk_test_fail_total_recharged();`, u.ID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DROP TRIGGER tk_test_fail_total_recharged_trg ON users`)
		_, _ = integrationDB.Exec(`DROP FUNCTION tk_test_fail_total_recharged()`)
		_, _ = integrationDB.Exec(`DELETE FROM redeem_codes WHERE used_by = $1`, u.ID)
		_, _ = integrationDB.Exec(`DELETE FROM users WHERE id = $1`, u.ID)
	})
	_, err = adminSvc.UpdateUserBalance(ctx, u.ID, 10, "add", "operator")
	require.ErrorContains(t, err, "forced total update failure")
	stored, err := userRepo.GetByID(ctx, u.ID)
	require.NoError(t, err)
	require.Equal(t, 1.0, stored.Balance)
	require.Zero(t, stored.TotalRecharged)
	var count int
	require.NoError(t, integrationDB.QueryRow(`SELECT COUNT(*) FROM redeem_codes WHERE used_by = $1`, u.ID).Scan(&count))
	require.Zero(t, count)
}

func TestTotalRechargedBackfill_QualifyingAndIdempotent(t *testing.T) {
	tx := testTx(t)
	// Shadow the real tables inside this transaction so the migration cannot
	// rewrite unrelated fixtures. PostgreSQL still executes the shipped SQL.
	_, err := tx.Exec(`
CREATE TEMP TABLE users (LIKE public.users INCLUDING DEFAULTS) ON COMMIT DROP;
CREATE TEMP TABLE redeem_codes (LIKE public.redeem_codes INCLUDING DEFAULTS) ON COMMIT DROP;
INSERT INTO users (id, email, password_hash, balance, total_recharged, deleted_at) VALUES
  (1, 'paid@example.com', 'hash', 1, 0, NULL),
  (2, 'gift@example.com', 'hash', 1, 99, NULL),
  (3, 'empty@example.com', 'hash', 1, 99, NULL),
  (4, 'deleted@example.com', 'hash', 1, 99, now());`)
	require.NoError(t, err)
	seed := func(userID int, typ string, amount float64, notes any) {
		t.Helper()
		code, err := service.GenerateRedeemCode()
		require.NoError(t, err)
		_, err = tx.Exec(`INSERT INTO redeem_codes (code, type, value, status, used_by, notes)
VALUES ($1, $2, $3, 'used', $4, $5)`, code, typ, amount, userID, notes)
		require.NoError(t, err)
	}
	seed(1, service.RedeemTypeBalance, 50, nil)
	seed(1, service.AdjustmentTypeAdminBalance, 100, service.BalanceGrantNoteAdminOpening)
	seed(1, service.AdjustmentTypeAdminBalance, 1000, "operator")
	seed(1, service.AdjustmentTypeAdminBalance, 5, " "+service.BalanceGrantNoteSignup+" ")
	seed(1, service.AdjustmentTypeAdminBalance, -20, "operator")
	seed(1, service.RedeemTypeConcurrency, 10, nil)
	for _, note := range service.GiftBalanceGrantNotes() {
		seed(1, service.AdjustmentTypeAdminBalance, 2, note)
		seed(2, service.AdjustmentTypeAdminBalance, 2, note)
	}
	migration, err := dbmigrations.FS.ReadFile("tk_102_backfill_total_recharged_qualifying.sql")
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = tx.Exec(string(migration))
		require.NoError(t, err)
		for id, want := range map[int]float64{1: 1155, 2: 0, 3: 0, 4: 99} {
			var balance, total float64
			require.NoError(t, tx.QueryRow(`SELECT balance, total_recharged FROM users WHERE id = $1`, id).Scan(&balance, &total))
			require.Equal(t, 1.0, balance)
			require.Equal(t, want, total)
		}
	}
}
