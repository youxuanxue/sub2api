package service

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Guards the D1 straddling-partition reclaim's per-run cap: the chunked DELETE
// must stop once it has removed maxRows, so the first reclaim of a large legacy
// backlog cannot turn into a runaway delete that hammers prod in a single cleanup
// run. A revert of the cap is otherwise invisible until it bites under load.
func TestDeleteOldRowsByID_RespectsMaxRowsCap(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// The remaining cap is smaller than batchSize. Passing the full batch would
	// let PostgreSQL delete 5000 rows despite the advertised 3-row hard limit.
	mock.ExpectExec("DELETE FROM ops_test").
		WithArgs(time.Unix(0, 0).UTC(), 3).
		WillReturnResult(sqlmock.NewResult(0, 3))

	total, err := deleteOldRowsByID(context.Background(), db, "ops_test", "created_at", time.Unix(0, 0).UTC(), 5000, false, 3)
	require.NoError(t, err)
	require.Equal(t, int64(3), total)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Lifecycle tables are required schema. A missing relation must fail the unified
// cleanup heartbeat instead of being reported as a successful zero-row cleanup.
func TestDeleteOldRowsByID_MissingRelationFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectExec("DELETE FROM ops_test").
		WillReturnError(assert.AnError)

	_, err = deleteOldRowsByID(context.Background(), db, "ops_test", "created_at", time.Unix(0, 0).UTC(), 5000, false, 0)
	require.ErrorIs(t, err, assert.AnError)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Unbounded mode (maxRows=0, the path used for non-partitioned tables) must keep
// deleting until a batch affects zero rows.
func TestDeleteOldRowsByID_UnboundedDrainsUntilEmpty(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectExec("DELETE FROM ops_test").WillReturnResult(sqlmock.NewResult(0, 5000))
	mock.ExpectExec("DELETE FROM ops_test").WillReturnResult(sqlmock.NewResult(0, 5000))
	mock.ExpectExec("DELETE FROM ops_test").WillReturnResult(sqlmock.NewResult(0, 0))

	total, err := deleteOldRowsByID(context.Background(), db, "ops_test", "created_at", time.Unix(0, 0).UTC(), 5000, false, 0)
	require.NoError(t, err)
	require.Equal(t, int64(10000), total)
	require.NoError(t, mock.ExpectationsWereMet())
}
