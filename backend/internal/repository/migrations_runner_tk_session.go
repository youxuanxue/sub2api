package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

const (
	migrationSessionTimezone       = "UTC"
	migrationSessionRestoreTimeout = 5 * time.Second
)

// migrationSessionCleanupError marks failures that make a pooled PostgreSQL
// session unsafe to reuse, such as a failed advisory unlock.
type migrationSessionCleanupError struct {
	err error
}

func (e *migrationSessionCleanupError) Error() string {
	return e.err.Error()
}

func (e *migrationSessionCleanupError) Unwrap() error {
	return e.err
}

// applyMigrationsInUTCSession owns the timezone contract for every migration
// caller. Partition bounds must not depend on the application's business
// timezone, and the pinned connection must be restored before it returns to
// the pool.
func applyMigrationsInUTCSession(ctx context.Context, conn *sql.Conn, fsys fs.FS) (retErr error) {
	var originalTimezone string
	if err := conn.QueryRowContext(ctx, "SHOW TIME ZONE").Scan(&originalTimezone); err != nil {
		return fmt.Errorf("read migration session timezone: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT set_config('TimeZone', $1, false)", migrationSessionTimezone); err != nil {
		setupErr := &migrationSessionCleanupError{
			err: fmt.Errorf("set migration session timezone: %w", err),
		}
		return errors.Join(setupErr, discardMigrationSession(conn))
	}

	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.Background(), migrationSessionRestoreTimeout)
		defer cancel()
		if _, err := conn.ExecContext(restoreCtx, "SELECT set_config('TimeZone', $1, false)", originalTimezone); err != nil {
			retErr = errors.Join(retErr, &migrationSessionCleanupError{
				err: fmt.Errorf("restore migration session timezone: %w", err),
			})
		}
		var cleanupErr *migrationSessionCleanupError
		if errors.As(retErr, &cleanupErr) {
			retErr = errors.Join(retErr, discardMigrationSession(conn))
		}
	}()

	return applyMigrationsSession(ctx, conn, fsys)
}

func discardMigrationSession(conn *sql.Conn) error {
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); err != nil && !errors.Is(err, driver.ErrBadConn) {
		return fmt.Errorf("discard unsafe migration session: %w", err)
	}
	return nil
}
