//go:build integration

package archive

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestUS045_SQLControlStoreSetsOnlyApprovedForwardCutover(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(
		ctx, "postgres:18.1-alpine3.23",
		postgres.WithDatabase("qa_archive_cutover_store"),
		postgres.WithUsername("postgres"), postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("start postgres: %v", err)
	}
	defer func() { _ = container.Terminate(ctx) }()
	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	for _, migration := range []string{
		"tk_069_create_qa_archive_shards.sql",
		"tk_070_qa_archive_closeout_control.sql",
		"tk_072_qa_archive_forward_cutover.sql",
	} {
		body, readErr := migrations.FS.ReadFile(migration)
		require.NoError(t, readErr)
		_, execErr := db.ExecContext(ctx, string(body))
		require.NoError(t, execErr)
	}
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	control := NewSQLControlStore()
	approved := Phase2ForwardCutoverWindow()
	reset := func(t *testing.T) {
		t.Helper()
		_, resetErr := db.ExecContext(ctx, `TRUNCATE qa_archive_shards RESTART IDENTITY CASCADE`)
		require.NoError(t, resetErr)
	}
	insert := func(t *testing.T, window Window, state string, restoreVerified bool, marked bool) int64 {
		t.Helper()
		var restoreAt any
		if restoreVerified {
			restoreAt = window.End.Add(time.Minute)
		}
		var id int64
		require.NoError(t, db.QueryRowContext(ctx, `
INSERT INTO qa_archive_shards (
    window_start, window_end, generation, state, restore_verified_at, forward_cutover
) VALUES ($1,$2,0,$3,$4,$5)
RETURNING id`, window.Start, window.End, state, restoreAt, marked).Scan(&id))
		return id
	}

	t.Run("absent target", func(t *testing.T) {
		reset(t)
		got, ok, err := control.ReadForwardCutover(ctx, conn)
		require.NoError(t, err)
		require.False(t, ok)
		require.Zero(t, got)
		_, err = control.SetApprovedForwardCutover(ctx, conn)
		require.ErrorContains(t, err, "does not exist")
	})

	t.Run("invalid state", func(t *testing.T) {
		reset(t)
		insert(t, approved, StatePending, true, false)
		_, err := control.SetApprovedForwardCutover(ctx, conn)
		require.ErrorContains(t, err, "must be committed")
	})

	t.Run("restore not verified", func(t *testing.T) {
		reset(t)
		insert(t, approved, StateCommitted, false, false)
		_, err := control.SetApprovedForwardCutover(ctx, conn)
		require.ErrorContains(t, err, "restore-verified")
	})

	t.Run("invalid exact window", func(t *testing.T) {
		reset(t)
		malformed := Window{Start: approved.Start, End: approved.End.Add(time.Hour)}
		insert(t, malformed, StateCommitted, true, false)
		_, err := control.SetApprovedForwardCutover(ctx, conn)
		require.ErrorContains(t, err, "exact approved window")
	})

	t.Run("exact setting is idempotent", func(t *testing.T) {
		reset(t)
		id := insert(t, approved, StateCommitted, true, false)
		first, err := control.SetApprovedForwardCutover(ctx, conn)
		require.NoError(t, err)
		require.Equal(t, id, first.ShardID)
		require.Equal(t, approved, first.Window)

		var firstUpdatedAt time.Time
		require.NoError(t, db.QueryRowContext(ctx, `SELECT updated_at FROM qa_archive_shards WHERE id=$1`, id).Scan(&firstUpdatedAt))
		second, err := control.SetApprovedForwardCutover(ctx, conn)
		require.NoError(t, err)
		require.Equal(t, first, second)
		var secondUpdatedAt time.Time
		require.NoError(t, db.QueryRowContext(ctx, `SELECT updated_at FROM qa_archive_shards WHERE id=$1`, id).Scan(&secondUpdatedAt))
		require.Equal(t, firstUpdatedAt, secondUpdatedAt, "idempotent retry must not rewrite the row")

		got, ok, err := control.ReadForwardCutover(ctx, conn)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, first, got)
	})

	t.Run("different existing marker rejects move", func(t *testing.T) {
		reset(t)
		insert(t, approved, StateCommitted, true, false)
		other := Window{Start: approved.Start.Add(time.Hour), End: approved.End.Add(time.Hour)}
		insert(t, other, StateCommitted, true, true)
		_, err := control.SetApprovedForwardCutover(ctx, conn)
		require.ErrorContains(t, err, "move is forbidden")
		_, _, readErr := control.ReadForwardCutover(ctx, conn)
		require.ErrorContains(t, readErr, "exact approved window")
	})

	t.Run("corrupt duplicate markers fail closed", func(t *testing.T) {
		reset(t)
		_, err := db.ExecContext(ctx, `DROP INDEX idx_qa_archive_shards_one_forward_cutover`)
		require.NoError(t, err)
		insert(t, approved, StateCommitted, true, true)
		other := Window{Start: approved.Start.Add(time.Hour), End: approved.End.Add(time.Hour)}
		insert(t, other, StateCommitted, true, true)
		_, _, err = control.ReadForwardCutover(ctx, conn)
		require.ErrorContains(t, err, "multiple marked rows")
	})
}
