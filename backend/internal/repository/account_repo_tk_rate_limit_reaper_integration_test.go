//go:build integration

package repository

// TK: See upstream Wei-Shaw/sub2api#2538 — integration tests for the
// reaper's SQL-side contract. These cover the three failure modes the
// service-layer fakes cannot prove: (1) the predicate window is inclusive on
// the right and exclusive on the left, (2) soft-deleted accounts are skipped,
// and (3) the existing 1s outbox dedup window protects against double-enqueue
// when consecutive reaper ticks overlap.

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// resetSchedulerOutbox truncates the outbox table so each test starts at a
// known state. The integration suite shares a database, so we cannot rely on
// the suite-level rollback.
func resetSchedulerOutbox(t *testing.T) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(),
		"TRUNCATE scheduler_outbox RESTART IDENTITY")
	require.NoError(t, err)
}

// insertAccountWithRateLimitReset inserts a minimal account row directly via
// SQL so the test owns the rate_limit_reset_at value. We avoid the ent
// Account.Create path because it would force us to populate every required
// field even when the test only cares about (rate_limit_reset_at, deleted_at).
func insertAccountWithRateLimitReset(t *testing.T, name string, resetAt *time.Time, deleted bool) int64 {
	t.Helper()
	ctx := context.Background()

	// Defensive cleanup: remove any leftover row with the same name so reruns
	// stay deterministic.
	_, err := integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE name = $1", name)
	require.NoError(t, err)

	var id int64
	var deletedAt *time.Time
	if deleted {
		now := time.Now()
		deletedAt = &now
	}
	err = integrationDB.QueryRowContext(ctx, `
		INSERT INTO accounts (
			name, platform, type, status, schedulable, priority, concurrency,
			credentials, extra, rate_limit_reset_at, deleted_at,
			created_at, updated_at
		) VALUES (
			$1, 'anthropic', 'oauth', 'active', TRUE, 50, 1,
			'{}'::jsonb, '{}'::jsonb, $2, $3, NOW(), NOW()
		)
		RETURNING id
	`, name, resetAt, deletedAt).Scan(&id)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(),
			"DELETE FROM accounts WHERE id = $1", id)
	})
	return id
}

// TestEnqueueOutboxForJustExpiredAccounts_RepoEnqueuesAccountChangedForExpired
// is the core #2538 invariant: an account whose cooldown just expired must
// land in scheduler_outbox as a single `account_changed` event. Without this
// row, the outbox worker never rebuilds the bucket and the account stays
// invisible to scheduling.
func TestEnqueueOutboxForJustExpiredAccounts_RepoEnqueuesAccountChangedForExpired(t *testing.T) {
	resetSchedulerOutbox(t)

	now := time.Now()
	expiredAt := now.Add(-5 * time.Second) // expired 5s ago
	id := insertAccountWithRateLimitReset(t, "rl-reaper-expired", &expiredAt, false)

	repo := NewRateLimitExpiryRepository(integrationDB)

	inserted, err := repo.EnqueueOutboxForJustExpiredAccounts(t.Context(),
		now.Add(-30*time.Second), now)
	require.NoError(t, err)
	require.Equal(t, 1, inserted)

	var rows int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM scheduler_outbox
		 WHERE event_type = $1 AND account_id = $2`,
		service.SchedulerOutboxEventAccountChanged, id).Scan(&rows))
	require.Equal(t, 1, rows,
		"reaper must enqueue exactly one account_changed event per expired account")
}

// TestEnqueueOutboxForJustExpiredAccounts_RepoSkipsStillCoolingDownAccounts
// pins the right-bound: accounts whose cooldown has NOT yet elapsed must
// stay out of the outbox. Without this guard the reaper would prematurely
// re-add accounts to snapshots while they are still rate-limited.
func TestEnqueueOutboxForJustExpiredAccounts_RepoSkipsStillCoolingDownAccounts(t *testing.T) {
	resetSchedulerOutbox(t)

	now := time.Now()
	futureReset := now.Add(60 * time.Second) // still cooling down
	insertAccountWithRateLimitReset(t, "rl-reaper-cooling", &futureReset, false)

	repo := NewRateLimitExpiryRepository(integrationDB)
	inserted, err := repo.EnqueueOutboxForJustExpiredAccounts(t.Context(),
		now.Add(-30*time.Second), now)
	require.NoError(t, err)
	require.Equal(t, 0, inserted, "cooling-down accounts must NOT trigger reaper enqueue")
}

// TestEnqueueOutboxForJustExpiredAccounts_RepoSkipsLongExpiredOutsideWindow
// pins the left-bound: accounts that expired far in the past (outside the
// (since, until] window) must be skipped by this tick. A subsequent reaper
// tick with a wider window can still pick them up if needed.
func TestEnqueueOutboxForJustExpiredAccounts_RepoSkipsLongExpiredOutsideWindow(t *testing.T) {
	resetSchedulerOutbox(t)

	now := time.Now()
	longAgo := now.Add(-2 * time.Hour)
	insertAccountWithRateLimitReset(t, "rl-reaper-long-ago", &longAgo, false)

	repo := NewRateLimitExpiryRepository(integrationDB)
	inserted, err := repo.EnqueueOutboxForJustExpiredAccounts(t.Context(),
		now.Add(-30*time.Second), now)
	require.NoError(t, err)
	require.Equal(t, 0, inserted,
		"accounts expired outside the (since, until] window must be skipped this tick")
}

// TestEnqueueOutboxForJustExpiredAccounts_RepoSkipsSoftDeletedAccounts
// confirms the deleted_at IS NULL guard.
func TestEnqueueOutboxForJustExpiredAccounts_RepoSkipsSoftDeletedAccounts(t *testing.T) {
	resetSchedulerOutbox(t)

	now := time.Now()
	expiredAt := now.Add(-5 * time.Second)
	insertAccountWithRateLimitReset(t, "rl-reaper-deleted", &expiredAt, true)

	repo := NewRateLimitExpiryRepository(integrationDB)
	inserted, err := repo.EnqueueOutboxForJustExpiredAccounts(t.Context(),
		now.Add(-30*time.Second), now)
	require.NoError(t, err)
	require.Equal(t, 0, inserted,
		"soft-deleted accounts must NOT be re-added by the reaper")
}

// TestEnqueueOutboxForJustExpiredAccounts_RepoDedupsRepeatedTicksWithin1Second
// pins the dedup contract: when two reaper ticks fire within the 1-second
// outbox dedup window, the second tick must NOT enqueue a duplicate
// account_changed row for the same expired account.
func TestEnqueueOutboxForJustExpiredAccounts_RepoDedupsRepeatedTicksWithin1Second(t *testing.T) {
	resetSchedulerOutbox(t)

	now := time.Now()
	expiredAt := now.Add(-2 * time.Second)
	id := insertAccountWithRateLimitReset(t, "rl-reaper-dedup", &expiredAt, false)

	repo := NewRateLimitExpiryRepository(integrationDB)
	first, err := repo.EnqueueOutboxForJustExpiredAccounts(t.Context(),
		now.Add(-30*time.Second), now)
	require.NoError(t, err)
	require.Equal(t, 1, first)

	// Immediately re-run: the existing 1s outbox dedup clause must kick in.
	second, err := repo.EnqueueOutboxForJustExpiredAccounts(t.Context(),
		now.Add(-30*time.Second), now)
	require.NoError(t, err)
	require.Equal(t, 0, second,
		"reaper ticks within the 1s outbox dedup window must not double-enqueue")

	var rows int
	require.NoError(t, integrationDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM scheduler_outbox
		 WHERE event_type = $1 AND account_id = $2`,
		service.SchedulerOutboxEventAccountChanged, id).Scan(&rows))
	require.Equal(t, 1, rows,
		"after two back-to-back reaper ticks, exactly one outbox row should exist")
}

// TestEnqueueOutboxForJustExpiredAccounts_RepoInvertedWindowIsNoOp guards
// against a misconfigured caller passing since >= until — the SQL must short-
// circuit before scanning the table.
func TestEnqueueOutboxForJustExpiredAccounts_RepoInvertedWindowIsNoOp(t *testing.T) {
	resetSchedulerOutbox(t)

	now := time.Now()
	expiredAt := now.Add(-5 * time.Second)
	insertAccountWithRateLimitReset(t, "rl-reaper-inverted", &expiredAt, false)

	repo := NewRateLimitExpiryRepository(integrationDB)
	// since > until: must be a no-op.
	inserted, err := repo.EnqueueOutboxForJustExpiredAccounts(t.Context(),
		now.Add(10*time.Second), now)
	require.NoError(t, err)
	require.Equal(t, 0, inserted)
}
