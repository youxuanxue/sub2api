package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestGetLatestUsedAtByUserIDs_QueryShapeAndSemantics(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	newer := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)SELECT user_id, MAX\(created_at\) AS last_used_at.*GROUP BY user_id`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "last_used_at"}).AddRow(int64(11), newer))

	repo := &userRepository{sql: db}
	got, err := repo.GetLatestUsedAtByUserIDs(context.Background(), []int64{11, 22})
	require.NoError(t, err)
	require.Contains(t, got, int64(11))
	require.NotContains(t, got, int64(22))
	require.True(t, got[11].Equal(newer))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserLastUsedAtOrderSubquery_UsesOrderByLimitNotMax(t *testing.T) {
	got := userLastUsedAtOrderSubquery("users.id")
	require.Contains(t, got, "WHERE user_id = users.id")
	require.Contains(t, got, "ORDER BY created_at DESC LIMIT 1")
	require.NotContains(t, got, "MAX(created_at)")
}
