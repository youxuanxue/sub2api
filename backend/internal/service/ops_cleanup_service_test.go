package service

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
)

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

	expectCleanupTable(t, mock, "ops_error_logs", 14, 3)
	expectCleanupTable(t, mock, "ops_retry_attempts", 14, 2)
	expectCleanupTable(t, mock, "ops_alert_events", 14, 1)
	expectCleanupTable(t, mock, "ops_system_logs", 14, 5)
	expectCleanupTable(t, mock, "ops_system_log_cleanup_audits", 14, 4)

	counts, err := svc.runCleanupOnce(context.Background())
	if err != nil {
		t.Fatalf("runCleanupOnce() error = %v", err)
	}
	if counts.errorLogs != 3 || counts.retryAttempts != 2 || counts.alertEvents != 1 {
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
