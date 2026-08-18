package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestTerminalOutcomeRepositoryFlushMinuteCommitsFactsAndHealthAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewTerminalOutcomeRepository(db)
	bucket := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	flush := service.TerminalOutcomeMinuteFlush{
		Facts: []service.TerminalOutcomeFact{{
			BucketStart: bucket, GroupID: 7, RequestedModel: "gpt-5.4", ProducerEpoch: "epoch-a",
			SuccessCount: 4, EmptyPool429Count: 2, OtherErrorCount: 1,
		}},
		Health: service.TerminalOutcomeHealth{
			BucketStart: bucket, ProducerEpoch: "epoch-a", ProcessStartedAt: bucket,
			FlushSequence: 1, ClosedAt: bucket.Add(2 * time.Minute),
			SeenCount: 7, PersistedCount: 7, Complete: true,
		},
	}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO channel_monitor_v2_terminal_outcomes_1m").
		WithArgs(bucket, int64(7), "gpt-5.4", "epoch-a", int64(4), int64(2), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO channel_monitor_v2_terminal_ingestion_health_1m").
		WithArgs(bucket, "epoch-a", bucket, int64(1), bucket.Add(2*time.Minute), int64(7), int64(7), int64(0), int64(0), true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.FlushMinute(context.Background(), flush))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTerminalOutcomeRepositoryRollsBackWhenHealthWriteFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := NewTerminalOutcomeRepository(db)
	bucket := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	flush := service.TerminalOutcomeMinuteFlush{
		Health: service.TerminalOutcomeHealth{
			BucketStart: bucket, ProducerEpoch: "epoch-a", ProcessStartedAt: bucket,
			FlushSequence: 1, ClosedAt: bucket.Add(2 * time.Minute), Complete: true,
		},
	}

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO channel_monitor_v2_terminal_ingestion_health_1m").
		WithArgs(bucket, "epoch-a", bucket, int64(1), bucket.Add(2*time.Minute), int64(0), int64(0), int64(0), int64(0), true).
		WillReturnError(errors.New("write failed"))
	mock.ExpectRollback()

	require.Error(t, repo.FlushMinute(context.Background(), flush))
	require.NoError(t, mock.ExpectationsWereMet())
}
