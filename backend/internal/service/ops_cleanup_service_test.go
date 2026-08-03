package service

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
)

type opsCleanupHeartbeatCapture struct {
	opsRepoMock
	heartbeats []*OpsUpsertJobHeartbeatInput
}

func (c *opsCleanupHeartbeatCapture) UpsertJobHeartbeat(_ context.Context, input *OpsUpsertJobHeartbeatInput) error {
	c.heartbeats = append(c.heartbeats, input)
	return nil
}

type cutoffDaysArg struct {
	days int
}

func (a cutoffDaysArg) Match(v driver.Value) bool {
	t, ok := v.(time.Time)
	if !ok {
		return false
	}
	age := time.Since(t)
	want := time.Duration(a.days) * 24 * time.Hour
	return age >= want-time.Minute && age <= want+time.Minute
}

func expectCleanupTable(t *testing.T, mock sqlmock.Sqlmock, table string, cutoffDays int, deleted int64) {
	t.Helper()
	// opsCleanupRunOne first checks whether the table is partitioned; a plain table
	// (false) falls through to the chunked DELETE below. (A partitioned table would
	// instead ensure future partitions + DROP expired ones — covered by the
	// pgpartition integration tests.)
	mock.ExpectQuery("pg_partitioned_table").
		WithArgs(table).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec(table).
		WithArgs(cutoffDaysArg{days: cutoffDays}, 5000).
		WillReturnResult(sqlmock.NewResult(0, deleted))
	mock.ExpectExec(table).
		WithArgs(cutoffDaysArg{days: cutoffDays}, 5000).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestOpsCleanupServiceRunCleanupOnceUsesSeparateLogRetentions(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	// After upstream refactor (d218b6c2): all log tables (error + system) share
	// ErrorLogRetentionDays. SystemLogRetentionDays is kept in config for backwards compat
	// but not used by the cleanup executor. days < 0 → skip (days == 0 → TRUNCATE).
	cfg := &config.Config{
		Ops: config.OpsConfig{
			Cleanup: config.OpsCleanupConfig{
				ErrorLogRetentionDays:      14,
				MinuteMetricsRetentionDays: -1,
				HourlyMetricsRetentionDays: -1,
			},
		},
	}
	svc := NewOpsCleanupService(&opsRepoMock{}, db, nil, cfg, nil, nil)
	svc.refreshEffectiveBeforeRun(context.Background())

	// Upstream Wei-Shaw/sub2api commit 2eb622f2 dropped ops_retry_attempts
	// alongside the retry/replay feature; the cleanup loop no longer touches it.
	expectCleanupTable(t, mock, "ops_error_logs", 14, 3)
	expectCleanupTable(t, mock, "ops_alert_events", 14, 1)
	expectCleanupTable(t, mock, "ops_system_logs", 14, 5)
	expectCleanupTable(t, mock, "ops_system_log_cleanup_audits", 14, 4)

	counts, err := svc.runCleanupOnce(context.Background())
	if err != nil {
		t.Fatalf("runCleanupOnce() error = %v", err)
	}
	if counts.errorLogs != 3 || counts.alertEvents != 1 {
		t.Fatalf("unexpected error-like cleanup counts: %+v", counts)
	}
	if counts.systemLogs != 5 || counts.logAudits != 4 {
		t.Fatalf("unexpected system cleanup counts: %+v", counts)
	}
	if counts.systemMetrics != 0 || counts.hourlyPreagg != 0 || counts.dailyPreagg != 0 {
		t.Fatalf("metrics cleanup should be disabled in this test: %+v", counts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestOpsCleanupScheduled_DisabledStillMaintainsPartitionsWithoutCleanupHeartbeat(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	for _, table := range []string{"ops_system_logs", "ops_error_logs"} {
		mock.ExpectQuery("pg_partitioned_table").WithArgs(table).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		for i := 0; i <= opsPartitionMonthsAhead; i++ {
			mock.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
		}
	}
	mock.ExpectQuery("pg_partitioned_table").WithArgs("usage_logs").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	repo := &opsCleanupHeartbeatCapture{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Ops.Cleanup.Enabled = false
	cfg.Ops.Cleanup.Schedule = opsCleanupDefaultSchedule
	svc := NewOpsCleanupService(repo, db, nil, cfg, nil, nil)
	svc.refreshEffectiveBeforeRun(context.Background())
	svc.runScheduled()

	if len(repo.heartbeats) != 1 || repo.heartbeats[0].JobName != opsPartitionJobName {
		t.Fatalf("heartbeats=%+v, want only %s", repo.heartbeats, opsPartitionJobName)
	}
	if repo.heartbeats[0].LastSuccessAt == nil || repo.heartbeats[0].LastErrorAt != nil {
		t.Fatalf("partition heartbeat should be successful: %+v", repo.heartbeats[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestOpsCleanupScheduled_PartitionFailureStopsBeforeCleanup(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery("pg_partitioned_table").WithArgs("ops_system_logs").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnError(errors.New("disk full"))

	repo := &opsCleanupHeartbeatCapture{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Ops.Cleanup.Enabled = true
	cfg.Ops.Cleanup.Schedule = opsCleanupDefaultSchedule
	svc := NewOpsCleanupService(repo, db, nil, cfg, nil, nil)
	svc.refreshEffectiveBeforeRun(context.Background())
	svc.runScheduled()

	if len(repo.heartbeats) != 1 || repo.heartbeats[0].JobName != opsPartitionJobName {
		t.Fatalf("heartbeats=%+v, want only failed %s", repo.heartbeats, opsPartitionJobName)
	}
	if repo.heartbeats[0].LastErrorAt == nil || repo.heartbeats[0].LastSuccessAt != nil {
		t.Fatalf("partition heartbeat should record the error: %+v", repo.heartbeats[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
