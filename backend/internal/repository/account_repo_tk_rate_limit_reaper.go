package repository

// TK: See upstream Wei-Shaw/sub2api#2538 — RateLimitExpiryRepository
// implementation backed by a single atomic INSERT ... SELECT statement.
//
// Why one SQL: each tick of SchedulerRateLimitReaper must (a) find every
// account whose `rate_limit_reset_at` falls in the (since, until] window AND
// (b) enqueue one outbox `account_changed` event per match. Doing both in a
// single statement avoids race conditions where an account could be selected
// by the reaper and then have its rate limit re-set by a 429 between the
// SELECT and the INSERT — the INSERT...SELECT lets PostgreSQL evaluate the
// predicate atomically against committed state.
//
// The outbox table already has a 1-second dedup window for `account_changed`
// events (see scheduler_outbox_repo.go::enqueueSchedulerOutbox), so reaper
// ticks shorter than 1s are safe — duplicates get silently dropped by the
// existing WHERE NOT EXISTS clause.

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type rateLimitExpiryRepository struct {
	db *sql.DB
}

// NewRateLimitExpiryRepository constructs the reaper-facing repository. The
// implementation talks directly to the shared *sql.DB so it does not depend on
// AccountRepository — adding a new method to that interface would force every
// stub/mock in the codebase to grow a no-op implementation (CLAUDE.md rule 6).
func NewRateLimitExpiryRepository(db *sql.DB) service.RateLimitExpiryRepository {
	return &rateLimitExpiryRepository{db: db}
}

// EnqueueOutboxForJustExpiredAccounts inserts one
// scheduler_outbox(event_type='account_changed') row per account whose
// `rate_limit_reset_at` falls inside (since, until], excluding soft-deleted
// rows. The outbox INSERT is bounded by the existing 1-second WHERE NOT
// EXISTS dedup clause so concurrent reaper invocations or rapid ticks stay
// idempotent. Returns the number of rows inserted.
func (r *rateLimitExpiryRepository) EnqueueOutboxForJustExpiredAccounts(ctx context.Context, since, until time.Time) (int, error) {
	if r == nil || r.db == nil {
		return 0, nil
	}
	if !since.Before(until) {
		// Defensive: never run an inverted-window query (would return zero
		// rows but still costs a sequential scan on production-sized tables).
		return 0, nil
	}

	// INSERT...SELECT in a single statement so the predicate evaluation and
	// the outbox writes happen against the same MVCC snapshot. The dedup
	// clause mirrors scheduler_outbox_repo.go::enqueueSchedulerOutbox so
	// reaper-written events stay consistent with SetRateLimited-written
	// events.
	const query = `
		INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
		SELECT 'account_changed', a.id, NULL, NULL
		FROM accounts AS a
		WHERE a.rate_limit_reset_at > $1
		  AND a.rate_limit_reset_at <= $2
		  AND a.deleted_at IS NULL
		  AND NOT EXISTS (
			SELECT 1
			FROM scheduler_outbox AS o
			WHERE o.event_type = 'account_changed'
			  AND o.account_id IS NOT DISTINCT FROM a.id
			  AND o.group_id IS NULL
			  AND o.created_at >= NOW() - make_interval(secs => 1)
		  )
	`
	res, err := r.db.ExecContext(ctx, query, since, until)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}
