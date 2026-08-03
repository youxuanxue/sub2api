package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/telemetryarchive"
	"github.com/stretchr/testify/require"
)

type captureTelemetrySink struct {
	datasets []telemetryarchive.Dataset
	values   []any
}

func (s *captureTelemetrySink) Enqueue(dataset telemetryarchive.Dataset, value any) bool {
	s.datasets = append(s.datasets, dataset)
	s.values = append(s.values, value)
	return true
}

func TestUsageTelemetryEnqueuesOnlyAfterSuccessfulInsert(t *testing.T) {
	tests := []struct {
		name       string
		queryRows  *sqlmock.Rows
		queryError error
		wantEvents int
	}{
		{
			name:       "inserted",
			queryRows:  sqlmock.NewRows([]string{"id", "created_at"}).AddRow(7, time.Now().UTC()),
			wantEvents: 1,
		},
		{name: "database error", queryError: sql.ErrConnDone, wantEvents: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			sink := &captureTelemetrySink{}
			repo := &usageLogRepository{sql: db, telemetry: sink}
			expectation := mock.ExpectQuery("INSERT INTO usage_logs")
			if test.queryError != nil {
				expectation.WillReturnError(test.queryError)
			} else {
				expectation.WillReturnRows(test.queryRows)
			}

			_, err := repo.Create(context.Background(), &service.UsageLog{
				UserID: 1, APIKeyID: 2, AccountID: 3, Model: "model",
			})
			if test.queryError != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, sink.datasets, test.wantEvents)
			if test.wantEvents > 0 {
				require.Equal(t, telemetryarchive.DatasetUsage, sink.datasets[0])
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpsBatchTelemetryWaitsForCommit(t *testing.T) {
	tests := []struct {
		name        string
		commitError error
		wantEvents  int
	}{
		{name: "committed", wantEvents: 1},
		{name: "commit failed", commitError: errors.New("commit failed"), wantEvents: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			sink := &captureTelemetrySink{}
			repo := &opsRepository{db: db, telemetry: sink}
			mock.ExpectBegin()
			mock.ExpectPrepare("INSERT INTO ops_error_logs").
				ExpectExec().WillReturnResult(sqlmock.NewResult(1, 1))
			if test.commitError != nil {
				mock.ExpectCommit().WillReturnError(test.commitError)
			} else {
				mock.ExpectCommit()
			}

			_, err := repo.BatchInsertErrorLogs(
				context.Background(),
				[]*service.OpsInsertErrorLogInput{{
					ErrorPhase: "upstream", ErrorType: "upstream_error",
				}},
			)
			if test.commitError != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, sink.datasets, test.wantEvents)
			if test.wantEvents > 0 {
				require.Equal(t, telemetryarchive.DatasetOpsError, sink.datasets[0])
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
