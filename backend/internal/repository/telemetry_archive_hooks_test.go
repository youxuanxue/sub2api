package repository

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestUsageBestEffortBatchReportsOnlyTheInsertedDuplicate(t *testing.T) {
	db, mock := newSQLMock(t)
	sink := &captureTelemetrySink{}
	prepared := prepareUsageLogInsert(&service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: "same-request", Model: "model",
	})
	mock.ExpectQuery("INSERT INTO usage_logs").WillReturnRows(
		sqlmock.NewRows([]string{"request_id", "api_key_id"}).
			AddRow("same-request", int64(2)),
	)
	requests := []usageLogBestEffortRequest{
		{prepared: prepared, apiKeyID: 2, telemetryValue: &service.UsageLog{RequestID: "same-request"}, resultCh: make(chan usageLogCreateResult, 1)},
		{prepared: prepared, apiKeyID: 2, telemetryValue: &service.UsageLog{RequestID: "same-request"}, resultCh: make(chan usageLogCreateResult, 1)},
	}

	repo := &usageLogRepository{telemetry: sink}
	repo.flushBestEffortBatch(db, requests)
	first := <-requests[0].resultCh
	second := <-requests[1].resultCh

	require.NoError(t, first.err)
	require.True(t, first.inserted)
	require.NoError(t, second.err)
	require.False(t, second.inserted)
	require.Len(t, sink.values, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUS042UsageBestEffortLateCompletionStillEnqueuesTelemetry(t *testing.T) {
	db, _ := newSQLMock(t)
	repo := newUsageLogRepositoryWithSQL(nil, db)
	sink := &captureTelemetrySink{}
	repo.telemetry = sink
	repo.bestEffortBatchCh = make(chan usageLogBestEffortRequest, 1)

	userAgent := "original-agent"
	log := &service.UsageLog{
		UserID: 1, APIKeyID: 2, AccountID: 3,
		RequestID: " late-completion ", Model: "model",
		UserAgent: &userAgent, ImageSizeBreakdown: map[string]int{"1024x1024": 1},
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- repo.CreateBestEffort(ctx, log)
	}()

	req := <-repo.bestEffortBatchCh
	cancel()
	err := <-errCh
	require.Error(t, err)
	require.True(t, service.IsUsageLogCreateDropped(err))
	require.Empty(t, sink.values)

	log.RequestID = "caller-reused"
	*log.UserAgent = "mutated-agent"
	log.ImageSizeBreakdown["1024x1024"] = 9
	repo.completeUsageLogBestEffortRequest(req, usageLogCreateResult{inserted: true})

	require.Len(t, sink.values, 1)
	payload, err := json.Marshal(sink.values[0])
	require.NoError(t, err)
	var shadowed service.UsageLog
	require.NoError(t, json.Unmarshal(payload, &shadowed))
	require.Equal(t, "late-completion", shadowed.RequestID)
	require.Equal(t, "model", shadowed.RequestedModel)
	require.Equal(t, "original-agent", *shadowed.UserAgent)
	require.Equal(t, map[string]int{"1024x1024": 1}, shadowed.ImageSizeBreakdown)
	require.False(t, shadowed.CreatedAt.IsZero())
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

func TestOpsSystemTelemetryMatchesPersistedNormalization(t *testing.T) {
	db, mock := newSQLMock(t)
	sink := &captureTelemetrySink{}
	repo := &opsRepository{db: db, telemetry: sink}
	mock.ExpectBegin()
	copyStatement := mock.ExpectPrepare(`COPY "ops_system_logs"`)
	copyStatement.ExpectExec().WillReturnResult(sqlmock.NewResult(0, 1))
	copyStatement.ExpectExec().WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	createdAt := time.Date(2026, 8, 3, 9, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	input := &service.OpsInsertSystemLogInput{
		CreatedAt: createdAt,
		Level:     " WARN ",
		Component: " ",
		Message:   " persisted message ",
		ExtraJSON: " ",
	}

	inserted, err := repo.BatchInsertSystemLogs(
		context.Background(), []*service.OpsInsertSystemLogInput{input},
	)

	require.NoError(t, err)
	require.Equal(t, int64(1), inserted)
	require.Equal(t, []telemetryarchive.Dataset{telemetryarchive.DatasetOpsSystem}, sink.datasets)
	require.Len(t, sink.values, 1)
	shadowed, ok := sink.values[0].(*service.OpsInsertSystemLogInput)
	require.True(t, ok)
	require.Equal(t, createdAt.UTC(), shadowed.CreatedAt)
	require.Equal(t, "warn", shadowed.Level)
	require.Equal(t, "app", shadowed.Component)
	require.Equal(t, "persisted message", shadowed.Message)
	require.Equal(t, "{}", shadowed.ExtraJSON)
	require.Equal(t, " WARN ", input.Level, "normalization must not mutate the caller")
	require.NoError(t, mock.ExpectationsWereMet())
}
