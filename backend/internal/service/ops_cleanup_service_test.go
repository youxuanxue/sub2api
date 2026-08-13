package service

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/partitionmaintenance"
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
	return ok && cutoffMatchesDays(t, a.days)
}

func cutoffMatchesDays(t time.Time, days int) bool {
	age := time.Since(t)
	want := time.Duration(days) * 24 * time.Hour
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

	// Error and system evidence have separate lifecycle windows. days < 0 skips a
	// dataset, while the pre-existing days == 0 semantics remain TRUNCATE.
	cfg := &config.Config{
		Server: config.ServerConfig{FrontendURL: "https://api.tokenkey.dev"},
		Ops: config.OpsConfig{
			Cleanup: config.OpsCleanupConfig{
				SystemLogRetentionDays:     7,
				ErrorLogRetentionDays:      14,
				MinuteMetricsRetentionDays: -1,
				HourlyMetricsRetentionDays: -1,
			},
		},
		DashboardAgg: config.DashboardAggregationConfig{
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays:         90,
				UsageBillingDedupDays: 365,
			},
		},
	}
	dashboardRepo := &dashboardAggregationRepoTestStub{}
	svc := NewOpsCleanupService(&opsRepoMock{}, dashboardRepo, db, nil, cfg, nil, nil)
	svc.refreshEffectiveBeforeRun(context.Background())

	// Upstream Wei-Shaw/sub2api commit 2eb622f2 dropped ops_retry_attempts
	// alongside the retry/replay feature; the cleanup loop no longer touches it.
	expectCleanupTable(t, mock, "ops_error_logs", 14, 3)
	expectCleanupTable(t, mock, "ops_alert_events", 14, 1)
	expectCleanupTable(t, mock, "ops_system_logs", 7, 5)
	expectCleanupTable(t, mock, "ops_system_log_cleanup_audits", 7, 4)

	counts, err := svc.runCleanupOnce(context.Background())
	if err != nil {
		t.Fatalf("runCleanupOnce() error = %v", err)
	}
	if dashboardRepo.cleanupUsageCalls != 1 || dashboardRepo.cleanupDedupCalls != 1 {
		t.Fatalf("usage lifecycle calls = usage:%d dedup:%d, want 1 each", dashboardRepo.cleanupUsageCalls, dashboardRepo.cleanupDedupCalls)
	}
	if !cutoffMatchesDays(dashboardRepo.lastUsageCutoff, 90) || !cutoffMatchesDays(dashboardRepo.lastDedupCutoff, 365) {
		t.Fatalf("unexpected usage cutoffs: usage=%s dedup=%s", dashboardRepo.lastUsageCutoff, dashboardRepo.lastDedupCutoff)
	}
	if counts.usageLogs != "ok" || counts.billingDedup != "ok" {
		t.Fatalf("unexpected usage cleanup states: %+v", counts)
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

func TestOpsCleanupServiceRunCleanupOnceUsageFailureStopsBeforeOpsTables(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	dashboardRepo := &dashboardAggregationRepoTestStub{cleanupUsageErr: errors.New("usage unavailable")}
	cfg := &config.Config{
		Server: config.ServerConfig{FrontendURL: "https://api-us1.tokenkey.dev"},
		Ops: config.OpsConfig{Cleanup: config.OpsCleanupConfig{
			SystemLogRetentionDays:     7,
			ErrorLogRetentionDays:      30,
			MinuteMetricsRetentionDays: -1,
			HourlyMetricsRetentionDays: -1,
		}},
		DashboardAgg: config.DashboardAggregationConfig{Retention: config.DashboardAggregationRetentionConfig{
			UsageLogsDays:         90,
			UsageBillingDedupDays: 365,
		}},
	}
	svc := NewOpsCleanupService(&opsRepoMock{}, dashboardRepo, db, nil, cfg, nil, nil)
	svc.refreshEffectiveBeforeRun(context.Background())

	_, err = svc.runCleanupOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cleanup usage_logs") {
		t.Fatalf("runCleanupOnce() error = %v, want usage cleanup failure", err)
	}
	if dashboardRepo.cleanupUsageCalls != 1 || dashboardRepo.cleanupDedupCalls != 0 {
		t.Fatalf("usage lifecycle calls = usage:%d dedup:%d", dashboardRepo.cleanupUsageCalls, dashboardRepo.cleanupDedupCalls)
	}
	if !cutoffMatchesDays(dashboardRepo.lastUsageCutoff, 7) {
		t.Fatalf("edge usage cutoff = %s, want 7 days", dashboardRepo.lastUsageCutoff)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected ops table cleanup: %v", err)
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
		for i := 0; i < 4; i++ {
			mock.ExpectExec("CREATE TABLE IF NOT EXISTS").WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectQuery("(?s)pg_get_expr.*pg_inherits").
			WithArgs(table, sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"covered_ranges"}).AddRow(4))
	}
	mock.ExpectQuery("pg_partitioned_table").WithArgs("usage_logs").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	repo := &opsCleanupHeartbeatCapture{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Ops.Cleanup.Enabled = false
	cfg.Ops.Cleanup.Schedule = opsCleanupDefaultSchedule
	svc := NewOpsCleanupService(repo, nil, db, nil, cfg, nil, nil)
	svc.refreshEffectiveBeforeRun(context.Background())
	svc.runScheduled()

	if len(repo.heartbeats) != 1 || repo.heartbeats[0].JobName != partitionmaintenance.JobName {
		t.Fatalf("heartbeats=%+v, want only %s", repo.heartbeats, partitionmaintenance.JobName)
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
	svc := NewOpsCleanupService(repo, nil, db, nil, cfg, nil, nil)
	svc.refreshEffectiveBeforeRun(context.Background())
	svc.runScheduled()

	if len(repo.heartbeats) != 1 || repo.heartbeats[0].JobName != partitionmaintenance.JobName {
		t.Fatalf("heartbeats=%+v, want only failed %s", repo.heartbeats, partitionmaintenance.JobName)
	}
	if repo.heartbeats[0].LastErrorAt == nil || repo.heartbeats[0].LastSuccessAt != nil {
		t.Fatalf("partition heartbeat should record the error: %+v", repo.heartbeats[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
