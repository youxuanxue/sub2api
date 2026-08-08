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

func TestUS045_SQLTimelineSelectorPersistsTerminalAndFindsUncoveredIdentity(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(
		ctx, "postgres:18.1-alpine3.23",
		postgres.WithDatabase("qa_archive_timeline"),
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
		"tk_004_create_qa_records.sql",
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
	cutover := Phase2ForwardCutoverWindow()

	reset := func(t *testing.T) {
		t.Helper()
		_, resetErr := db.ExecContext(ctx, `
TRUNCATE qa_archive_segment_records, qa_archive_segments, qa_archive_shards RESTART IDENTITY CASCADE;
TRUNCATE qa_records;`)
		require.NoError(t, resetErr)
		_, resetErr = db.ExecContext(ctx, `
INSERT INTO qa_archive_shards (
    window_start, window_end, generation, state, restore_verified_at, forward_cutover
) VALUES ($1,$2,0,'committed',$2,true)`, cutover.Start, cutover.End)
		require.NoError(t, resetErr)
	}

	t.Run("expired missing hour becomes durable terminal control", func(t *testing.T) {
		reset(t)
		window := Window{Start: cutover.End, End: cutover.End.Add(time.Hour)}
		normal := Window{Start: window.End, End: window.End.Add(time.Hour)}
		selection, ok, selectErr := SelectOldestCatchup(ctx, conn, control, normal, window.Start.Add(30*time.Minute))
		require.NoError(t, selectErr)
		require.True(t, ok)
		require.Equal(t, CatchupDispositionSourceUnavailableAfterRetention, selection.Disposition)
		require.Equal(t, window, selection.Window)
		var state, code string
		var cleanupEligible bool
		require.NoError(t, db.QueryRowContext(ctx, `
SELECT state, verification_error_code, cleanup_eligible
FROM qa_archive_shards WHERE window_start=$1 AND generation=0`, window.Start).Scan(&state, &code, &cleanupEligible))
		require.Equal(t, StateFailed, state)
		require.Equal(t, IntegritySourceUnavailableAfterRetention, code)
		require.False(t, cleanupEligible)
	})

	t.Run("committed uncovered identity qualifies until membership converges", func(t *testing.T) {
		reset(t)
		late := Window{Start: cutover.End, End: cutover.End.Add(time.Hour)}
		complete := Window{Start: late.End, End: late.End.Add(time.Hour)}
		var shardID int64
		require.NoError(t, db.QueryRowContext(ctx, `
INSERT INTO qa_archive_shards (window_start, window_end, generation, state, restore_verified_at)
VALUES ($1,$2,0,'committed',$2) RETURNING id`, late.Start, late.End).Scan(&shardID))
		_, err = db.ExecContext(ctx, `
INSERT INTO qa_archive_shards (window_start, window_end, generation, state, restore_verified_at)
VALUES ($1,$2,0,'committed',$2)`, complete.Start, complete.End)
		require.NoError(t, err)
		var segmentID int64
		require.NoError(t, db.QueryRowContext(ctx, `
INSERT INTO qa_archive_segments (
    shard_id, segment_id, segment_kind, state, attempt_id, manifest_key, records_key
) VALUES ($1,'base-late','base','committed','base-late','manifest','records')
RETURNING id`, shardID).Scan(&segmentID))
		coveredAt := late.Start.Add(5 * time.Minute)
		uncoveredAt := late.Start.Add(10 * time.Minute)
		for _, row := range []struct {
			requestID string
			createdAt time.Time
		}{{"covered", coveredAt}, {"uncovered", uncoveredAt}} {
			_, err = db.ExecContext(ctx, `
INSERT INTO qa_records (request_id, user_id, api_key_id, created_at, retention_until)
VALUES ($1,1,1,$2,$3)`, row.requestID, row.createdAt, row.createdAt.Add(24*time.Hour))
			require.NoError(t, err)
		}
		_, err = db.ExecContext(ctx, `
INSERT INTO qa_archive_segment_records (segment_id, created_at, request_id)
VALUES ($1,$2,'covered')`, segmentID, coveredAt)
		require.NoError(t, err)

		normal := Window{Start: complete.End, End: complete.End.Add(time.Hour)}
		selection, ok, selectErr := SelectOldestCatchup(ctx, conn, control, normal, cutover.Start)
		require.NoError(t, selectErr)
		require.True(t, ok)
		require.Equal(t, late, selection.Window)
		require.Equal(t, CatchupDispositionReconcile, selection.Disposition)

		_, err = db.ExecContext(ctx, `
INSERT INTO qa_archive_segment_records (segment_id, created_at, request_id)
VALUES ($1,$2,'uncovered')`, segmentID, uncoveredAt)
		require.NoError(t, err)
		_, ok, selectErr = SelectOldestCatchup(ctx, conn, control, normal, cutover.Start)
		require.NoError(t, selectErr)
		require.False(t, ok)
	})

	t.Run("terminal classification rechecks source inside its transaction", func(t *testing.T) {
		reset(t)
		window := Window{Start: cutover.End, End: cutover.End.Add(time.Hour)}
		var shardID int64
		require.NoError(t, db.QueryRowContext(ctx, `
INSERT INTO qa_archive_shards (window_start, window_end, generation, state, cleanup_eligible)
VALUES ($1,$2,0,'pending',false) RETURNING id`, window.Start, window.End).Scan(&shardID))
		_, err = db.ExecContext(ctx, `
INSERT INTO qa_records (request_id, user_id, api_key_id, created_at, retention_until)
VALUES ('late-before-terminal',1,1,$1,$2)`, window.Start.Add(10*time.Minute), window.End.Add(24*time.Hour))
		require.NoError(t, err)

		_, classifyErr := control.MarkSourceUnavailableAfterRetention(ctx, conn, window)
		require.ErrorContains(t, classifyErr, "source rows still exist")
		var state string
		var code sql.NullString
		require.NoError(t, db.QueryRowContext(ctx, `
SELECT state, verification_error_code
FROM qa_archive_shards WHERE id=$1`, shardID).Scan(&state, &code))
		require.Equal(t, StatePending, state)
		require.False(t, code.Valid)
	})
}
