package archive

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const phase2ForwardCutoverLockID int64 = 5821032861721

type ForwardCutover struct {
	ShardID           int64
	Window            Window
	RestoreVerifiedAt time.Time
}

type forwardCutoverRecord struct {
	ForwardCutover
	Generation int
	State      string
}

type cutoverQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func Phase2ForwardCutoverWindow() Window {
	start := time.Date(2026, 8, 7, 21, 0, 0, 0, time.UTC)
	return Window{Start: start, End: start.Add(time.Hour)}
}

func (s *SQLControlStore) ReadForwardCutover(ctx context.Context, conn *sql.Conn) (ForwardCutover, bool, error) {
	if conn == nil {
		return ForwardCutover{}, false, fmt.Errorf("read forward cutover: nil database connection")
	}
	records, err := queryForwardCutovers(ctx, conn, "WHERE forward_cutover", false)
	if err != nil {
		return ForwardCutover{}, false, fmt.Errorf("read forward cutover: %w", err)
	}
	if len(records) == 0 {
		return ForwardCutover{}, false, nil
	}
	if len(records) != 1 {
		return ForwardCutover{}, false, fmt.Errorf("read forward cutover: multiple marked rows")
	}
	if err := validateApprovedForwardCutover(records[0]); err != nil {
		return ForwardCutover{}, false, fmt.Errorf("read forward cutover: %w", err)
	}
	return records[0].ForwardCutover, true, nil
}

func (s *SQLControlStore) SetApprovedForwardCutover(ctx context.Context, conn *sql.Conn) (ForwardCutover, error) {
	if conn == nil {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: nil database connection")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", phase2ForwardCutoverLockID); err != nil {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: acquire lock: %w", err)
	}

	marked, err := queryForwardCutovers(ctx, tx, "WHERE forward_cutover", true)
	if err != nil {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: inspect existing marker: %w", err)
	}
	if len(marked) > 1 {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: multiple marked rows")
	}
	if len(marked) == 1 {
		if !isApprovedForwardCutoverWindow(marked[0]) {
			return ForwardCutover{}, fmt.Errorf("set forward cutover: marker exists on a different window; move is forbidden")
		}
		if err := validateApprovedForwardCutover(marked[0]); err != nil {
			return ForwardCutover{}, fmt.Errorf("set forward cutover: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return ForwardCutover{}, fmt.Errorf("set forward cutover: commit idempotent read: %w", err)
		}
		return marked[0].ForwardCutover, nil
	}

	approved := Phase2ForwardCutoverWindow()
	targets, err := queryForwardCutovers(
		ctx,
		tx,
		"WHERE window_start=$1 AND generation=0",
		true,
		approved.Start,
	)
	if err != nil {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: read approved shard: %w", err)
	}
	if len(targets) == 0 {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: approved shard does not exist")
	}
	if len(targets) != 1 {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: approved shard is ambiguous")
	}
	target := targets[0]
	if err := validateApprovedForwardCutover(target); err != nil {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE qa_archive_shards
SET forward_cutover=true, updated_at=now()
WHERE id=$1 AND forward_cutover=false`, target.ShardID)
	if err != nil {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: mark approved shard: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: inspect update: %w", err)
	}
	if changed != 1 {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: approved shard changed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return ForwardCutover{}, fmt.Errorf("set forward cutover: commit: %w", err)
	}
	return target.ForwardCutover, nil
}

func queryForwardCutovers(
	ctx context.Context,
	queryer cutoverQueryer,
	clause string,
	lock bool,
	args ...any,
) ([]forwardCutoverRecord, error) {
	query := `
SELECT id, window_start, window_end, generation, state, restore_verified_at
FROM qa_archive_shards
` + clause + `
ORDER BY id
LIMIT 2`
	if lock {
		query += " FOR UPDATE"
	}
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	records := make([]forwardCutoverRecord, 0, 2)
	for rows.Next() {
		var record forwardCutoverRecord
		var restoreVerifiedAt sql.NullTime
		if err := rows.Scan(
			&record.ShardID,
			&record.Window.Start,
			&record.Window.End,
			&record.Generation,
			&record.State,
			&restoreVerifiedAt,
		); err != nil {
			return nil, err
		}
		if restoreVerifiedAt.Valid {
			record.RestoreVerifiedAt = restoreVerifiedAt.Time
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func validateApprovedForwardCutover(record forwardCutoverRecord) error {
	if !isApprovedForwardCutoverWindow(record) {
		return fmt.Errorf("forward cutover must match the exact approved window")
	}
	if record.State != StateCommitted {
		return fmt.Errorf("forward cutover shard must be committed")
	}
	if record.RestoreVerifiedAt.IsZero() {
		return fmt.Errorf("forward cutover shard must be restore-verified")
	}
	return nil
}

func isApprovedForwardCutoverWindow(record forwardCutoverRecord) bool {
	approved := Phase2ForwardCutoverWindow()
	return record.Generation == 0 &&
		record.Window.Start.Equal(approved.Start) &&
		record.Window.End.Equal(approved.End)
}
