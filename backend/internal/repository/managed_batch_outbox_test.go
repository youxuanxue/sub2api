package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestManagedBatchOutboxListAfterBuildsExecutableJob(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	r := NewManagedBatchRepository(db)
	rows := sqlmock.NewRows([]string{"id", "batch_id", "payload"}).AddRow(int64(7), "batch-7", []byte(`{"batch_id":"batch-7","user_id":11,"api_key_id":13}`))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id,batch_id,payload FROM batch_outbox")).WithArgs(int64(0), 10).WillReturnRows(rows)
	events, err := r.ListAfter(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, int64(7), events[0].ID)
	require.Equal(t, "batch-7", events[0].Job.ID)
	require.JSONEq(t, `{"batch_id":"batch-7","user_id":11,"api_key_id":13}`, string(events[0].Job.Payload))
	require.NoError(t, mock.ExpectationsWereMet())
}
