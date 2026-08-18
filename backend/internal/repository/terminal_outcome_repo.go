package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type TerminalOutcomeRepository struct {
	db *sql.DB
}

func NewTerminalOutcomeRepository(db *sql.DB) *TerminalOutcomeRepository {
	return &TerminalOutcomeRepository{db: db}
}

func (r *TerminalOutcomeRepository) FlushMinute(ctx context.Context, flush service.TerminalOutcomeMinuteFlush) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin terminal outcome flush: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, fact := range flush.Facts {
		if _, err = tx.ExecContext(ctx, terminalOutcomeFactUpsertSQL,
			fact.BucketStart, fact.GroupID, fact.RequestedModel, fact.ProducerEpoch,
			fact.SuccessCount, fact.EmptyPool429Count, fact.OtherErrorCount,
		); err != nil {
			return fmt.Errorf("upsert terminal outcome fact: %w", err)
		}
	}
	health := flush.Health
	if _, err = tx.ExecContext(ctx, terminalOutcomeHealthUpsertSQL,
		health.BucketStart, health.ProducerEpoch, health.ProcessStartedAt, health.FlushSequence,
		health.ClosedAt, health.SeenCount, health.PersistedCount, health.DropCount,
		health.FlushFailureCount, health.Complete,
	); err != nil {
		return fmt.Errorf("upsert terminal outcome health: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit terminal outcome flush: %w", err)
	}
	return nil
}

const terminalOutcomeFactUpsertSQL = `
INSERT INTO channel_monitor_v2_terminal_outcomes_1m (
  bucket_start, group_id, requested_model, producer_epoch,
  success_count, final_empty_pool_429_count, other_error_count, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
ON CONFLICT (bucket_start, group_id, requested_model, producer_epoch) DO UPDATE SET
  success_count = EXCLUDED.success_count,
  final_empty_pool_429_count = EXCLUDED.final_empty_pool_429_count,
  other_error_count = EXCLUDED.other_error_count,
  updated_at = NOW()`

const terminalOutcomeHealthUpsertSQL = `
INSERT INTO channel_monitor_v2_terminal_ingestion_health_1m (
  bucket_start, producer_epoch, process_started_at, flush_sequence, closed_at,
  seen_count, persisted_count, drop_count, flush_failure_count, complete, heartbeat_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
ON CONFLICT (bucket_start, producer_epoch) DO UPDATE SET
  process_started_at = EXCLUDED.process_started_at,
  flush_sequence = EXCLUDED.flush_sequence,
  closed_at = EXCLUDED.closed_at,
  seen_count = EXCLUDED.seen_count,
  persisted_count = EXCLUDED.persisted_count,
  drop_count = EXCLUDED.drop_count,
  flush_failure_count = EXCLUDED.flush_failure_count,
  complete = EXCLUDED.complete,
  heartbeat_at = NOW()`
