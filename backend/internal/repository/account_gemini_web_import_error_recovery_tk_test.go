package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImportGeminiWebSessionRecoversErrorStatusAndPreservesSchedulableGate(t *testing.T) {
	var importSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(
		func(expectedSQL, actualSQL string) error {
			if strings.Contains(actualSQL, "UPDATE accounts") {
				importSQL = actualSQL
			}
			return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
		},
	)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := newAccountRepositoryWithSQL(nil, db, nil)

	mock.ExpectExec(`UPDATE accounts`).
		WithArgs(sqlmock.AnyArg(), int64(5), int64(13)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO scheduler_outbox`).
		WithArgs(service.SchedulerOutboxEventAccountChanged, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	ok, err := repo.ImportGeminiWebSession(context.Background(), 13, 5, map[string]any{
		"user_agent": "ua",
		"cookies":    []any{},
		"state":      map[string]any{"blocked": false},
	})
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, mock.ExpectationsWereMet())

	require.Contains(t, importSQL, "status = CASE WHEN status = 'error' THEN 'active' ELSE status END")
	require.Contains(t, importSQL, "error_message = CASE WHEN status = 'error' THEN '' ELSE error_message END")
	setClause := importSQL[:strings.Index(importSQL, "WHERE")]
	require.NotContains(t, setClause, "schedulable", "import must not toggle scheduling; AC-010 pause stays operator-owned")
}

func TestImportGeminiWebSessionConflictSkipsErrorRecoverySideEffects(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	mock.ExpectExec(`UPDATE accounts`).
		WithArgs(sqlmock.AnyArg(), int64(5), int64(13)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	ok, err := repo.ImportGeminiWebSession(context.Background(), 13, 5, map[string]any{"user_agent": "ua"})
	require.NoError(t, err)
	require.False(t, ok, "CAS miss must not clear status or enqueue scheduler recovery")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestClearGeminiWebSessionPauseResetsPendingWithoutTouchingCookies(t *testing.T) {
	var pauseSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(
		func(expectedSQL, actualSQL string) error {
			if strings.Contains(actualSQL, "generation_pending") {
				pauseSQL = actualSQL
			}
			return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
		},
	)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	mock.ExpectExec(`UPDATE accounts`).
		WithArgs(int64(13)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.clearGeminiWebSessionPause(context.Background(), 13))
	require.NoError(t, mock.ExpectationsWereMet())
	require.Contains(t, pauseSQL, "'{gemini_web,runtime,state,generation_pending}'")
	require.Contains(t, pauseSQL, "'{gemini_web,runtime,state,blocked}'")
	require.Contains(t, pauseSQL, "'{gemini_web,runtime,state,cooldown_until}'")
	require.NotContains(t, pauseSQL, "cookies")
	require.Contains(t, pauseSQL, "lease")
}

func TestClearGeminiWebSessionPauseNoopsWithoutSQL(t *testing.T) {
	repo := newAccountRepositoryWithSQL(nil, nil, nil)
	require.NoError(t, repo.clearGeminiWebSessionPause(context.Background(), 13))
	require.NoError(t, repo.clearGeminiWebSessionPause(context.Background(), 0))
}
