package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"testing/fstest"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestApplyMigrationsFS_UTCSessionRestoresOriginalTimezone(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	expectMigrationTimezoneSetup(mock, "Asia/Shanghai")
	prepareMigrationsBootstrapExpectations(mock)
	mock.ExpectExec("SELECT pg_advisory_unlock\\(\\$1\\)").
		WithArgs(migrationsAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMigrationTimezoneRestore(mock, "Asia/Shanghai")

	require.NoError(t, applyMigrationsFS(context.Background(), db, fstest.MapFS{}))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyMigrationsFS_UTCSessionRestoresAfterMigrationFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	migrationErr := errors.New("migration failed")
	expectMigrationTimezoneSetup(mock, "Asia/Shanghai")
	prepareMigrationsBootstrapExpectations(mock)
	mock.ExpectQuery("SELECT checksum FROM schema_migrations WHERE filename = \\$1").
		WithArgs("001_fail.sql").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectBegin()
	mock.ExpectExec("SELECT broken").WillReturnError(migrationErr)
	mock.ExpectRollback()
	mock.ExpectExec("SELECT pg_advisory_unlock\\(\\$1\\)").
		WithArgs(migrationsAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMigrationTimezoneRestore(mock, "Asia/Shanghai")

	fsys := fstest.MapFS{
		"001_fail.sql": &fstest.MapFile{Data: []byte("SELECT broken;")},
	}
	err = applyMigrationsFS(context.Background(), db, fsys)
	require.ErrorIs(t, err, migrationErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyMigrationsFS_UTCSessionRestoresAfterLockFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	lockErr := errors.New("lock failed")
	expectMigrationTimezoneSetup(mock, "Asia/Shanghai")
	mock.ExpectQuery("SELECT pg_try_advisory_lock\\(\\$1\\)").
		WithArgs(migrationsAdvisoryLockID).
		WillReturnError(lockErr)
	expectMigrationTimezoneRestore(mock, "Asia/Shanghai")

	err = applyMigrationsFS(context.Background(), db, fstest.MapFS{})
	require.ErrorIs(t, err, lockErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplyMigrationsFS_UTCSessionCleanupFailuresAreVisible(t *testing.T) {
	t.Run("unlock failure still restores timezone and poisons session", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		unlockErr := errors.New("unlock failed")
		expectMigrationTimezoneSetup(mock, "Asia/Shanghai")
		prepareMigrationsBootstrapExpectations(mock)
		mock.ExpectExec("SELECT pg_advisory_unlock\\(\\$1\\)").
			WithArgs(migrationsAdvisoryLockID).
			WillReturnError(unlockErr)
		expectMigrationTimezoneRestore(mock, "Asia/Shanghai")

		err = applyMigrationsFS(context.Background(), db, fstest.MapFS{})
		require.ErrorIs(t, err, unlockErr)
		var cleanupErr *migrationSessionCleanupError
		require.ErrorAs(t, err, &cleanupErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("restore failure preserves migration error and poisons session", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		defer func() { _ = db.Close() }()

		migrationErr := errors.New("migration failed")
		restoreErr := errors.New("restore failed")
		expectMigrationTimezoneSetup(mock, "Asia/Shanghai")
		prepareMigrationsBootstrapExpectations(mock)
		mock.ExpectQuery("SELECT checksum FROM schema_migrations WHERE filename = \\$1").
			WithArgs("001_fail.sql").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectBegin()
		mock.ExpectExec("SELECT broken").WillReturnError(migrationErr)
		mock.ExpectRollback()
		mock.ExpectExec("SELECT pg_advisory_unlock\\(\\$1\\)").
			WithArgs(migrationsAdvisoryLockID).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("SELECT set_config\\('TimeZone', \\$1, false\\)").
			WithArgs("Asia/Shanghai").
			WillReturnError(restoreErr)

		fsys := fstest.MapFS{
			"001_fail.sql": &fstest.MapFile{Data: []byte("SELECT broken;")},
		}
		err = applyMigrationsFS(context.Background(), db, fsys)
		require.ErrorIs(t, err, migrationErr)
		require.ErrorIs(t, err, restoreErr)
		require.ErrorContains(t, err, "restore migration session timezone")
		var cleanupErr *migrationSessionCleanupError
		require.ErrorAs(t, err, &cleanupErr)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestApplyMigrationsFS_UTCSessionSetupFailureDoesNotStartMigrations(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	setupErr := errors.New("set timezone failed")
	mock.ExpectQuery("SHOW TIME ZONE").
		WillReturnRows(sqlmock.NewRows([]string{"TimeZone"}).AddRow("Asia/Shanghai"))
	mock.ExpectExec("SELECT set_config\\('TimeZone', \\$1, false\\)").
		WithArgs(migrationSessionTimezone).
		WillReturnError(setupErr)

	err = applyMigrationsFS(context.Background(), db, fstest.MapFS{})
	require.ErrorIs(t, err, setupErr)
	require.ErrorContains(t, err, "set migration session timezone")
	var cleanupErr *migrationSessionCleanupError
	require.ErrorAs(t, err, &cleanupErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectMigrationTimezoneSetup(mock sqlmock.Sqlmock, original string) {
	mock.ExpectQuery("SHOW TIME ZONE").
		WillReturnRows(sqlmock.NewRows([]string{"TimeZone"}).AddRow(original))
	mock.ExpectExec("SELECT set_config\\('TimeZone', \\$1, false\\)").
		WithArgs(migrationSessionTimezone).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectMigrationTimezoneRestore(mock sqlmock.Sqlmock, original string) {
	mock.ExpectExec("SELECT set_config\\('TimeZone', \\$1, false\\)").
		WithArgs(original).
		WillReturnResult(sqlmock.NewResult(0, 1))
}
