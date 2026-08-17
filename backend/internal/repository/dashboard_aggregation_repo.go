package repository

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type dashboardAggregationRepository struct {
	sql   sqlExecutor
	clock func() time.Time
}

const usageLogsCleanupBatchSize = 10000
const usageBillingDedupCleanupBatchSize = 10000

// Automatic lifecycle cleanup drains large backlogs across existing cron runs rather
// than monopolizing the database in the first run after a retention-window change.
const usageLogsCleanupMaxRowsPerRun = 1_000_000
const usageBillingDedupCleanupMaxRowsPerRun = 1_000_000

// usageLogsStraddleReclaimMaxRowsPerRun caps expired-row DELETE from bound-straddling
// partitions (notably usage_logs_legacy) in one retention pass.
const usageLogsStraddleReclaimMaxRowsPerRun = usageLogsCleanupMaxRowsPerRun
const dashboardHistoricalBackfillMinRemaining = 5 * time.Minute

// NewDashboardAggregationRepository 创建仪表盘预聚合仓储。
func NewDashboardAggregationRepository(sqlDB *sql.DB) service.DashboardAggregationRepository {
	if sqlDB == nil {
		return nil
	}
	if !isPostgresDriver(sqlDB) {
		log.Printf("[DashboardAggregation] 检测到非 PostgreSQL 驱动，已自动禁用预聚合")
		return nil
	}
	return newDashboardAggregationRepositoryWithSQL(sqlDB)
}

func newDashboardAggregationRepositoryWithSQL(sqlq sqlExecutor) *dashboardAggregationRepository {
	return &dashboardAggregationRepository{sql: sqlq, clock: time.Now}
}

func (r *dashboardAggregationRepository) now() time.Time {
	if r.clock != nil {
		return r.clock()
	}
	return time.Now()
}

func hasDashboardHistoricalBackfillBudget(ctx context.Context) bool {
	deadline, ok := ctx.Deadline()
	return !ok || time.Until(deadline) >= dashboardHistoricalBackfillMinRemaining
}

func isPostgresDriver(db *sql.DB) bool {
	if db == nil {
		return false
	}
	_, ok := db.Driver().(*pq.Driver)
	return ok
}

func (r *dashboardAggregationRepository) AggregateRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	// TK: one-time historical backfill of the per-(group, day) rollup. The
	// watermark-driven incremental feeder below only moves forward, so the Groups
	// all-time usage-summary would never see pre-deploy history without this. Runs
	// once (guarded by emptiness); best-effort — on failure the read path keeps
	// the raw scan and the next cycle retries, so it never blocks aggregation.
	if err := r.backfillGroupDailyAllOnce(ctx); err != nil {
		log.Printf("[DashboardAggregation] group daily rollup backfill failed (read path falls back to raw scan): %v", err)
	}
	if err := r.backfillGroupDailyMetricsAllOnce(ctx); err != nil {
		log.Printf("[DashboardAggregation] group daily metrics backfill failed (group distribution falls back to raw scan): %v", err)
	}
	if err := r.backfillModelDailyAllOnce(ctx); err != nil {
		log.Printf("[DashboardAggregation] model daily rollup backfill failed (model distribution falls back to raw scan): %v", err)
	}
	loc := timezone.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := startLocal.Truncate(time.Hour)
	hourEnd := endLocal.Truncate(time.Hour)
	if endLocal.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}

	dayStart := truncateToDay(startLocal)
	dayEnd := truncateToDay(endLocal)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.Add(24 * time.Hour)
	}

	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		if err := txRepo.aggregateRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.aggregateRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd)
}

func (r *dashboardAggregationRepository) aggregateRangeInTx(ctx context.Context, hourStart, hourEnd, dayStart, dayEnd time.Time) error {
	// 以桶边界聚合，允许覆盖 end 所在桶的剩余区间。
	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.insertDailyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	// TK: per-(user, platform, day) rollup feeding the admin Users page +
	// dashboard spending-ranking widget. Aggregates the same day window from raw
	// usage_logs (see dashboard_aggregation_repo_tk_user_platform.go).
	if err := r.upsertUserPlatformDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	// TK: per-(group, day) cost rollup feeding the admin Groups usage-summary
	// (see dashboard_aggregation_repo_tk_group.go).
	if err := r.upsertGroupDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	// TK: per-(requested-model, day) rollup feeding dashboard model-stats widget.
	if err := r.upsertModelDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) RecomputeRange(ctx context.Context, start, end time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	loc := timezone.Location()
	startLocal := start.In(loc)
	endLocal := end.In(loc)
	if !endLocal.After(startLocal) {
		return nil
	}

	hourStart := startLocal.Truncate(time.Hour)
	hourEnd := endLocal.Truncate(time.Hour)
	if endLocal.After(hourEnd) {
		hourEnd = hourEnd.Add(time.Hour)
	}

	dayStart := truncateToDay(startLocal)
	dayEnd := truncateToDay(endLocal)
	if endLocal.After(dayEnd) {
		dayEnd = dayEnd.Add(24 * time.Hour)
	}

	// 尽量使用事务保证范围内的一致性（允许在非 *sql.DB 的情况下退化为非事务执行）。
	if db, ok := r.sql.(*sql.DB); ok {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := lockGroupUsageRollupState(ctx, tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := invalidateGroupUsageRollupsAt(ctx, tx, start); err != nil {
			_ = tx.Rollback()
			return err
		}
		txRepo := newDashboardAggregationRepositoryWithSQL(tx)
		if err := txRepo.recomputeRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := txRepo.syncGroupUsageRollupsInTx(ctx, service.GroupUsageTodayStart(r.now())); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.recomputeRangeInTx(ctx, hourStart, hourEnd, dayStart, dayEnd)
}

func (r *dashboardAggregationRepository) recomputeRangeInTx(ctx context.Context, hourStart, hourEnd, dayStart, dayEnd time.Time) error {
	// 先清空范围内桶，再重建（避免仅增量插入导致活跃用户等指标无法回退）。
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start >= $1 AND bucket_start < $2", hourStart, hourEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date >= $1::date AND bucket_date < $2::date", dayStart, dayEnd); err != nil {
		return err
	}
	// TK: clear the per-(user, platform, day) rollup for the window so a row that
	// dropped to zero after a usage_logs rollback does not linger before rebuild.
	if err := r.deleteUserPlatformDailyRange(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	// TK: clear the per-(group, day) rollup for the window before rebuild.
	if err := r.deleteGroupDailyRange(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.deleteModelDailyRange(ctx, dayStart, dayEnd); err != nil {
		return err
	}

	if err := r.insertHourlyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.insertDailyActiveUsers(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertHourlyAggregates(ctx, hourStart, hourEnd); err != nil {
		return err
	}
	if err := r.upsertDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	// TK: rebuild the per-(user, platform, day) rollup for the window.
	if err := r.upsertUserPlatformDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	// TK: rebuild the per-(group, day) cost rollup for the window.
	if err := r.upsertGroupDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	if err := r.upsertModelDailyAggregates(ctx, dayStart, dayEnd); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	var ts time.Time
	query := "SELECT last_aggregated_at FROM usage_dashboard_aggregation_watermark WHERE id = 1"
	if err := scanSingleRow(ctx, r.sql, query, nil, &ts); err != nil {
		if err == sql.ErrNoRows {
			return time.Unix(0, 0).UTC(), nil
		}
		return time.Time{}, err
	}
	return ts.UTC(), nil
}

func (r *dashboardAggregationRepository) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	query := `
		INSERT INTO usage_dashboard_aggregation_watermark (id, last_aggregated_at, updated_at)
		VALUES (1, $1, NOW())
		ON CONFLICT (id)
		DO UPDATE SET last_aggregated_at = EXCLUDED.last_aggregated_at, updated_at = EXCLUDED.updated_at
	`
	_, err := r.sql.ExecContext(ctx, query, aggregatedAt.UTC())
	return err
}

func (r *dashboardAggregationRepository) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	hourlyCutoffUTC := hourlyCutoff.UTC()
	dailyCutoffUTC := dailyCutoff.UTC()
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_hourly_users WHERE bucket_start < $1", hourlyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	if _, err := r.sql.ExecContext(ctx, "DELETE FROM usage_dashboard_daily_users WHERE bucket_date < $1::date", dailyCutoffUTC); err != nil {
		return err
	}
	// TK: prune the per-(user, platform, day) rollup with the same daily cutoff.
	if err := r.cleanupUserPlatformDaily(ctx, dailyCutoffUTC); err != nil {
		return err
	}
	return nil
}

func (r *dashboardAggregationRepository) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	isPartitioned, err := r.isUsageLogsPartitioned(ctx)
	if err != nil {
		return err
	}
	if isPartitioned {
		if err := r.cleanupPartitionedUsageLogs(ctx, cutoff.UTC()); err != nil {
			return err
		}
	} else if err := r.cleanupUsageLogsBatches(ctx, cutoff); err != nil {
		return err
	}
	return r.SyncGroupUsageRollups(ctx, service.GroupUsageTodayStart(r.now()))
}

func (r *dashboardAggregationRepository) cleanupPartitionedUsageLogs(ctx context.Context, cutoff time.Time) error {
	if err := r.dropUsageLogsPartitions(ctx, cutoff); err != nil {
		return err
	}
	db, ok := r.sql.(pgpartition.DB)
	if !ok {
		return nil
	}
	if _, err := pgpartition.DropExpired(ctx, db, "usage_logs", cutoff); err != nil {
		return err
	}
	straddling, err := pgpartition.ListStraddling(ctx, db, "usage_logs", "created_at", cutoff)
	if err != nil {
		return err
	}
	remaining := usageLogsStraddleReclaimMaxRowsPerRun
	for _, child := range straddling {
		if remaining <= 0 {
			break
		}
		n, delErr := deleteOldUsageLogRowsByID(ctx, db, child, cutoff, usageLogsCleanupBatchSize, remaining)
		if delErr != nil {
			return delErr
		}
		remaining -= int(n)
	}
	return nil
}

func deleteOldUsageLogRowsByID(
	ctx context.Context,
	db pgpartition.DropExecutor,
	table string,
	cutoff time.Time,
	batchSize int,
	maxRows int,
) (int64, error) {
	if db == nil {
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = usageLogsCleanupBatchSize
	}
	qTable := pq.QuoteIdentifier(table)
	q := fmt.Sprintf(`
WITH batch AS (
  SELECT id FROM %s
  WHERE created_at < $1
  ORDER BY id
  LIMIT $2
)
DELETE FROM %s
WHERE id IN (SELECT id FROM batch)
`, qTable, qTable)

	var total int64
	for {
		limit := batchSize
		if maxRows > 0 {
			remaining := maxRows - int(total)
			if remaining <= 0 {
				break
			}
			limit = min(limit, remaining)
		}
		res, err := db.ExecContext(ctx, q, cutoff, limit)
		if err != nil {
			return total, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
		if affected == 0 {
			break
		}
		if maxRows > 0 && total >= int64(maxRows) {
			break
		}
	}
	return total, nil
}

func (r *dashboardAggregationRepository) cleanupUsageLogsBatches(ctx context.Context, cutoff time.Time) error {
	db, transactional := r.sql.(*sql.DB)
	var total int64
	for {
		if transactional {
			affected, err := cleanupUsageLogsBatchWithRollupInvalidation(ctx, db, cutoff)
			if err != nil {
				return err
			}
			if affected < usageLogsCleanupBatchSize {
				return nil
			}
			continue
		}

		res, err := r.sql.ExecContext(ctx, `
			WITH victims AS (
				SELECT ctid
				FROM usage_logs
				WHERE created_at < $1
				ORDER BY created_at ASC, id ASC
				LIMIT $2
			)
			DELETE FROM usage_logs
			WHERE ctid IN (SELECT ctid FROM victims)
		`, cutoff.UTC(), usageLogsCleanupBatchSize)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		total += affected
		if affected < usageLogsCleanupBatchSize || total >= usageLogsCleanupMaxRowsPerRun {
			return nil
		}
	}
}

func cleanupUsageLogsBatchWithRollupInvalidation(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	rollback := func(err error) (int64, error) {
		_ = tx.Rollback()
		return 0, err
	}

	if err := lockGroupUsageRollupState(ctx, tx); err != nil {
		return rollback(err)
	}
	rows, err := tx.QueryContext(ctx, `
		WITH victims AS (
			SELECT ctid
			FROM usage_logs
			WHERE created_at < $1
			ORDER BY created_at ASC, id ASC
			LIMIT $2
		)
		DELETE FROM usage_logs
		WHERE ctid IN (SELECT ctid FROM victims)
		RETURNING created_at
	`, cutoff.UTC(), usageLogsCleanupBatchSize)
	if err != nil {
		return rollback(err)
	}

	var affected int64
	var earliestDeletedAt time.Time
	for rows.Next() {
		var deletedAt time.Time
		if err := rows.Scan(&deletedAt); err != nil {
			_ = rows.Close()
			return rollback(err)
		}
		affected++
		if earliestDeletedAt.IsZero() || deletedAt.Before(earliestDeletedAt) {
			earliestDeletedAt = deletedAt
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return rollback(err)
	}
	if err := rows.Close(); err != nil {
		return rollback(err)
	}
	if affected > 0 {
		if err := invalidateGroupUsageRollupsAt(ctx, tx, earliestDeletedAt); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

func (r *dashboardAggregationRepository) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	var total int64
	for {
		res, err := r.sql.ExecContext(ctx, `
			WITH victims AS (
				SELECT ctid, request_id, api_key_id, request_fingerprint, created_at
				FROM usage_billing_dedup
				WHERE created_at < $1
				LIMIT $2
			), archived AS (
				INSERT INTO usage_billing_dedup_archive (request_id, api_key_id, request_fingerprint, created_at)
				SELECT request_id, api_key_id, request_fingerprint, created_at
				FROM victims
				ON CONFLICT (request_id, api_key_id) DO NOTHING
			)
			DELETE FROM usage_billing_dedup
			WHERE ctid IN (SELECT ctid FROM victims)
		`, cutoff.UTC(), usageBillingDedupCleanupBatchSize)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		total += affected
		if affected < usageBillingDedupCleanupBatchSize || total >= usageBillingDedupCleanupMaxRowsPerRun {
			return nil
		}
	}
}

func (r *dashboardAggregationRepository) insertHourlyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_hourly_users (bucket_start, user_id)
		SELECT DISTINCT
			date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
			user_id
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) insertDailyActiveUsers(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		INSERT INTO usage_dashboard_daily_users (bucket_date, user_id)
		SELECT DISTINCT
			(bucket_start AT TIME ZONE $3)::date AS bucket_date,
			user_id
		FROM usage_dashboard_hourly_users
		WHERE bucket_start >= $1 AND bucket_start < $2
		ON CONFLICT DO NOTHING
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertHourlyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH hourly AS (
			SELECT
				date_trunc('hour', created_at AT TIME ZONE $3) AT TIME ZONE $3 AS bucket_start,
				COUNT(*) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) AS account_cost,
				COALESCE(SUM(COALESCE(duration_ms, 0)), 0) AS total_duration_ms,
				COALESCE(SUM(gateway_latency_ms) FILTER (WHERE gateway_latency_ms IS NOT NULL), 0) AS total_gateway_latency_ms,
				COUNT(gateway_latency_ms) FILTER (WHERE gateway_latency_ms IS NOT NULL) AS gateway_latency_samples
			FROM usage_logs
			WHERE created_at >= $1 AND created_at < $2
			GROUP BY 1
		),
		user_counts AS (
			SELECT bucket_start, COUNT(*) AS active_users
			FROM usage_dashboard_hourly_users
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY bucket_start
		)
		INSERT INTO usage_dashboard_hourly (
			bucket_start,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			total_duration_ms,
			total_gateway_latency_ms,
			gateway_latency_samples,
			active_users,
			computed_at
		)
		SELECT
			hourly.bucket_start,
			hourly.total_requests,
			hourly.input_tokens,
			hourly.output_tokens,
			hourly.cache_creation_tokens,
			hourly.cache_read_tokens,
			hourly.total_cost,
			hourly.actual_cost,
			hourly.account_cost,
			hourly.total_duration_ms,
			hourly.total_gateway_latency_ms,
			hourly.gateway_latency_samples,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM hourly
		LEFT JOIN user_counts ON user_counts.bucket_start = hourly.bucket_start
		ON CONFLICT (bucket_start)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			total_gateway_latency_ms = EXCLUDED.total_gateway_latency_ms,
			gateway_latency_samples = EXCLUDED.gateway_latency_samples,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) upsertDailyAggregates(ctx context.Context, start, end time.Time) error {
	tzName := timezone.Name()
	query := `
		WITH daily AS (
			SELECT
				(bucket_start AT TIME ZONE $5)::date AS bucket_date,
				COALESCE(SUM(total_requests), 0) AS total_requests,
				COALESCE(SUM(input_tokens), 0) AS input_tokens,
				COALESCE(SUM(output_tokens), 0) AS output_tokens,
				COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
				COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
				COALESCE(SUM(total_cost), 0) AS total_cost,
				COALESCE(SUM(actual_cost), 0) AS actual_cost,
				COALESCE(SUM(account_cost), 0) AS account_cost,
				COALESCE(SUM(total_duration_ms), 0) AS total_duration_ms,
				COALESCE(SUM(total_gateway_latency_ms), 0) AS total_gateway_latency_ms,
				COALESCE(SUM(gateway_latency_samples), 0) AS gateway_latency_samples
			FROM usage_dashboard_hourly
			WHERE bucket_start >= $1 AND bucket_start < $2
			GROUP BY (bucket_start AT TIME ZONE $5)::date
		),
		user_counts AS (
			SELECT bucket_date, COUNT(*) AS active_users
			FROM usage_dashboard_daily_users
			WHERE bucket_date >= $3::date AND bucket_date < $4::date
			GROUP BY bucket_date
		)
		INSERT INTO usage_dashboard_daily (
			bucket_date,
			total_requests,
			input_tokens,
			output_tokens,
			cache_creation_tokens,
			cache_read_tokens,
			total_cost,
			actual_cost,
			account_cost,
			total_duration_ms,
			total_gateway_latency_ms,
			gateway_latency_samples,
			active_users,
			computed_at
		)
		SELECT
			daily.bucket_date,
			daily.total_requests,
			daily.input_tokens,
			daily.output_tokens,
			daily.cache_creation_tokens,
			daily.cache_read_tokens,
			daily.total_cost,
			daily.actual_cost,
			daily.account_cost,
			daily.total_duration_ms,
			daily.total_gateway_latency_ms,
			daily.gateway_latency_samples,
			COALESCE(user_counts.active_users, 0) AS active_users,
			NOW()
		FROM daily
		LEFT JOIN user_counts ON user_counts.bucket_date = daily.bucket_date
		ON CONFLICT (bucket_date)
		DO UPDATE SET
			total_requests = EXCLUDED.total_requests,
			input_tokens = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			cache_read_tokens = EXCLUDED.cache_read_tokens,
			total_cost = EXCLUDED.total_cost,
			actual_cost = EXCLUDED.actual_cost,
			account_cost = EXCLUDED.account_cost,
			total_duration_ms = EXCLUDED.total_duration_ms,
			total_gateway_latency_ms = EXCLUDED.total_gateway_latency_ms,
			gateway_latency_samples = EXCLUDED.gateway_latency_samples,
			active_users = EXCLUDED.active_users,
			computed_at = EXCLUDED.computed_at
	`
	_, err := r.sql.ExecContext(ctx, query, start, end, start, end, tzName)
	return err
}

func (r *dashboardAggregationRepository) isUsageLogsPartitioned(ctx context.Context) (bool, error) {
	query := `
		SELECT EXISTS(
			SELECT 1
			FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'usage_logs'
		)
	`
	var partitioned bool
	if err := scanSingleRow(ctx, r.sql, query, nil, &partitioned); err != nil {
		return false, err
	}
	return partitioned, nil
}

func (r *dashboardAggregationRepository) dropUsageLogsPartitions(ctx context.Context, cutoff time.Time) error {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_inherits
		JOIN pg_class c ON c.oid = pg_inherits.inhrelid
		JOIN pg_class p ON p.oid = pg_inherits.inhparent
		WHERE p.relname = 'usage_logs'
	`)
	if err != nil {
		return err
	}
	cutoffMonth := truncateToMonthUTC(cutoff)
	type usageLogsPartition struct {
		name  string
		month time.Time
	}
	partitions := make([]usageLogsPartition, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		if !strings.HasPrefix(name, "usage_logs_") {
			continue
		}
		suffix := strings.TrimPrefix(name, "usage_logs_")
		month, err := time.Parse("200601", suffix)
		if err != nil {
			continue
		}
		month = month.UTC()
		if month.Before(cutoffMonth) {
			partitions = append(partitions, usageLogsPartition{name: name, month: month})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	sort.Slice(partitions, func(i, j int) bool {
		return partitions[i].month.Before(partitions[j].month)
	})
	if db, ok := r.sql.(*sql.DB); ok {
		for _, partition := range partitions {
			if err := dropUsageLogsPartitionWithRollupInvalidation(ctx, db, partition.name, partition.month); err != nil {
				return err
			}
		}
		return nil
	}
	for _, partition := range partitions {
		if _, err := r.sql.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(partition.name))); err != nil {
			return err
		}
	}
	return nil
}

func dropUsageLogsPartitionWithRollupInvalidation(ctx context.Context, db *sql.DB, name string, monthStart time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}

	if err := lockGroupUsageRollupState(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := invalidateGroupUsageRollupsAt(ctx, tx, monthStart); err != nil {
		return rollback(err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(name))); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func truncateToMonthUTC(t time.Time) time.Time {
	y, m, _ := t.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}

func truncateToDay(t time.Time) time.Time {
	return timezone.StartOfDay(t)
}
