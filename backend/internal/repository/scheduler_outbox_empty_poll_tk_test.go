package repository

import (
	"context"
	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSchedulerOutboxClaimsPendingWithoutWatermark(t *testing.T) {
	for _, empty := range []bool{true, false} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		repo := &schedulerOutboxRepository{db: db}
		mock.ExpectQuery(`SELECT EXISTS .*FROM scheduler_outbox.*acknowledged_at IS NULL`).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(!empty))
		rows := sqlmock.NewRows([]string{"id", "event_type", "account_id", "group_id", "payload", "created_at", "claim_token", "attempts"})
		if !empty {
			rows.AddRow(int64(2), "account_changed", nil, nil, []byte("{}"), time.Now(), "claim", 1)
		}
		if !empty {
			mock.ExpectQuery(`(?s)WITH selected AS MATERIALIZED.*acknowledged_at IS NULL.*FOR UPDATE SKIP LOCKED.*dedup_key = NULL.*SELECT \* FROM claimed ORDER BY id`).
				WithArgs(50, sqlmock.AnyArg(), int64(120000)).WillReturnRows(rows)
		}
		events, err := repo.ClaimPending(context.Background(), 50, 2*time.Minute)
		require.NoError(t, err)
		if empty {
			require.Empty(t, events)
		} else {
			require.Len(t, events, 1)
			require.EqualValues(t, 2, events[0].ID)
			require.Equal(t, "claim", events[0].ClaimToken)
		}
		require.NoError(t, mock.ExpectationsWereMet())
		mock.ExpectClose()
		require.NoError(t, db.Close())
	}
}
