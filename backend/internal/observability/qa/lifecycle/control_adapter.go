package lifecycle

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
)

// SQLControlAdapter adapts archive.SQLControlStore to TransitionControlStore.
type SQLControlAdapter struct {
	Store *archive.SQLControlStore
}

func NewSQLControlAdapter(store *archive.SQLControlStore) SQLControlAdapter {
	if store == nil {
		store = archive.NewSQLControlStore()
	}
	return SQLControlAdapter{Store: store}
}

func (a SQLControlAdapter) RecordSourceDropped(ctx context.Context, tx *sql.Tx, shardID int64, partitionName string, droppedAt time.Time) error {
	return a.Store.RecordSourceDropped(ctx, tx, shardID, partitionName, droppedAt)
}

func (a SQLControlAdapter) RecordHotFilesCleaned(ctx context.Context, conn *sql.Conn, shardID int64, cleanedAt time.Time, cleanupError string) error {
	return a.Store.RecordHotFilesCleaned(ctx, conn, shardID, cleanedAt, cleanupError)
}

func (a SQLControlAdapter) InspectCatchupHourTx(ctx context.Context, tx *sql.Tx, window archive.Window) (archive.CatchupHourStatus, error) {
	return a.Store.InspectCatchupHourTx(ctx, tx, window)
}

func (a SQLControlAdapter) PersistBoundaryTerminalGap(ctx context.Context, tx *sql.Tx, window archive.Window) (int64, error) {
	return a.Store.PersistBoundaryTerminalGap(ctx, tx, window)
}
