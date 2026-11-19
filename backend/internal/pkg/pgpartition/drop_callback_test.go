package pgpartition

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestDropExpiredWithCountsOnlyCompletedDrops(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	cutoff := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)SELECT.*pg_get_expr.*FROM pg_inherits`).WithArgs("usage_logs").WillReturnRows(
		sqlmock.NewRows([]string{"schema", "name", "bound", "upper", "rows"}).
			AddRow("public", "skipped", "finite", cutoff, 100).
			AddRow("other", `quoted"name`, "finite", cutoff, 7).
			AddRow("public", "future", "finite", cutoff.Add(time.Hour), 9))
	var names []string
	reclaimed, err := DropExpiredWith(context.Background(), db, "usage_logs", cutoff, func(name string) (bool, error) {
		names = append(names, name)
		return len(names) == 2, nil
	})
	require.NoError(t, err)
	require.Equal(t, int64(7), reclaimed)
	require.Equal(t, []string{`"public"."skipped"`, `"other"."quoted""name"`}, names)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDropExpiredWithRejectsUnknownBoundsBeforeCallback(t *testing.T) {
	for _, bound := range []string{"DEFAULT", "FOR VALUES FROM (MINVALUE) TO (MAXVALUE)"} {
		t.Run(bound, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			cutoff := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
			mock.ExpectQuery(`(?s)SELECT.*pg_get_expr.*FROM pg_inherits`).WithArgs("usage_logs").WillReturnRows(
				sqlmock.NewRows([]string{"schema", "name", "bound", "upper", "rows"}).
					AddRow("public", "expired", "finite", cutoff, 12).
					AddRow("public", "unknown", bound, nil, 5))
			called := false
			_, err = DropExpiredWith(context.Background(), db, "usage_logs", cutoff, func(string) (bool, error) {
				called = true
				return true, errors.New("callback must not run")
			})
			require.ErrorContains(t, err, "has no finite timestamptz upper bound")
			require.False(t, called)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
