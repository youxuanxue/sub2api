package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestSchedulerOutboxListAfterAndReleaseDedupSkipsClaimWhenEmpty(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}
	const existsSQL = `
		SELECT EXISTS (
			SELECT 1
			FROM scheduler_outbox
			WHERE id > $1
		)
	`
	mock.ExpectQuery(regexp.QuoteMeta(existsSQL)).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	events, err := repo.ListAfterAndReleaseDedup(context.Background(), 7, 50)
	require.NoError(t, err)
	require.Empty(t, events)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSchedulerOutboxListAfterAndReleaseDedupClaimsWhenRowsExist(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	repo := &schedulerOutboxRepository{db: db}
	const existsSQL = `
		SELECT EXISTS (
			SELECT 1
			FROM scheduler_outbox
			WHERE id > $1
		)
	`
	const claimSQL = `
		WITH selected AS MATERIALIZED (
			SELECT id, event_type, account_id, group_id, payload, created_at
			FROM scheduler_outbox
			WHERE id > $1
			ORDER BY id ASC
			LIMIT $2
			FOR UPDATE
		), released AS (
			UPDATE scheduler_outbox AS o
			SET dedup_key = NULL
			FROM selected AS s
			WHERE o.id = s.id
				AND o.dedup_key IS NOT NULL
			RETURNING o.id
		)
		SELECT s.id, s.event_type, s.account_id, s.group_id, s.payload, s.created_at
		FROM selected AS s
		CROSS JOIN (SELECT COUNT(*) FROM released) AS release_barrier
		ORDER BY s.id ASC
	`
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	mock.ExpectQuery(regexp.QuoteMeta(existsSQL)).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(claimSQL)).
		WithArgs(int64(7), 50).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "event_type", "account_id", "group_id", "payload", "created_at",
		}).AddRow(int64(8), "account_changed", nil, nil, []byte(`{}`), createdAt))

	events, err := repo.ListAfterAndReleaseDedup(context.Background(), 7, 50)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, int64(8), events[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}
