package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpsertJobHeartbeatPersistsErrorResult(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	mock.ExpectExec(regexp.QuoteMeta(
		"last_result = COALESCE(EXCLUDED.last_result, ops_job_heartbeats.last_result)",
	)).
		WithArgs(
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			`{"dropped":1,"failed":0}`,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectClose()

	now := time.Now().UTC()
	message := "dropped=1 failed=0"
	result := `{"dropped":1,"failed":0}`
	repo := &opsRepository{db: db}
	require.NoError(t, repo.UpsertJobHeartbeat(context.Background(), &service.OpsUpsertJobHeartbeatInput{
		JobName:     "telemetry_archive_shadow",
		LastRunAt:   &now,
		LastErrorAt: &now,
		LastError:   &message,
		LastResult:  &result,
	}))
	require.NoError(t, db.Close())
	require.NoError(t, mock.ExpectationsWereMet())
}
