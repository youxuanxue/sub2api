package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
)

func TestRequestLogRetention_RuntimePolicy(t *testing.T) {
	for _, tt := range []struct {
		name, raw string
		enabled   bool
		wantDays  int
	}{
		{"legacy uses deployment", `{"retention_days":30}`, true, 90},
		{"short window", `{"request_retention_days":7}`, true, 7},
		{"maximum", `{"request_retention_days":90}`, true, 90},
		{"works without aggregation", `{"request_retention_days":14}`, false, 14},
		{"legacy without aggregation", `{"retention_days":30}`, false, 90},
	} {
		t.Run(tt.name, func(t *testing.T) {
			settings := newRuntimeSettingRepoStub()
			settings.values[SettingKeyOpsRuntimeLogConfig] = tt.raw
			svc, repo := newRequestRetentionCleanup(t, settings)
			svc.cfg.DashboardAgg.Enabled = tt.enabled
			_, err := svc.runCleanupOnce(context.Background())
			require.NoError(t, err)
			require.Equal(t, 1, repo.cleanupUsageCalls)
			require.True(t, cutoffMatchesDays(repo.lastUsageCutoff, tt.wantDays))
			require.True(t, cutoffMatchesDays(repo.lastDedupCutoff, billingDedupRetentionDays))
			settings.values[SettingKeyOpsRuntimeLogConfig] = `{"request_retention_days":30}`
			_, err = svc.runCleanupOnce(context.Background())
			require.NoError(t, err)
			require.Equal(t, 2, repo.cleanupUsageCalls)
			require.True(t, cutoffMatchesDays(repo.lastUsageCutoff, 30))
		})
	}
}

func newRequestRetentionCleanup(t *testing.T, settings SettingRepository) (*OpsCleanupService, *dashboardAggregationRepoTestStub) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()); _ = db.Close() })
	cfg := &config.Config{DashboardAgg: config.DashboardAggregationConfig{
		Retention: config.DashboardAggregationRetentionConfig{UsageLogsDays: 90},
	}}
	repo := &dashboardAggregationRepoTestStub{}
	svc := NewOpsCleanupService(nil, repo, db, nil, cfg, nil, settings)
	// Isolate request/dedup retention from unrelated table maintenance.
	svc.effective.OpsCleanupConfig = config.OpsCleanupConfig{
		SystemLogRetentionDays: -1, ErrorLogRetentionDays: -1,
		MinuteMetricsRetentionDays: -1, HourlyMetricsRetentionDays: -1,
	}
	return svc, repo
}

func TestRequestLogRetention_ReadFailureSkipsDeletion(t *testing.T) {
	for _, raw := range []string{`{broken`, `{"request_retention_days":-1}`, `{"request_retention_days":0}`, `{"request_retention_days":91}`} {
		t.Run(raw, func(t *testing.T) {
			settings := newRuntimeSettingRepoStub()
			settings.values[SettingKeyOpsRuntimeLogConfig] = raw
			svc, repo := newRequestRetentionCleanup(t, settings)
			_, err := svc.runCleanupOnce(context.Background())
			require.Error(t, err)
			require.Zero(t, repo.cleanupUsageCalls)
			require.Zero(t, repo.cleanupDedupCalls)
		})
	}
	settings := newRuntimeSettingRepoStub()
	settings.getValueFn = func(string) (string, error) { return "", errors.New("database unavailable") }
	svc, repo := newRequestRetentionCleanup(t, settings)
	_, err := svc.runCleanupOnce(context.Background())
	require.ErrorContains(t, err, "database unavailable")
	require.Zero(t, repo.cleanupUsageCalls)
	require.Zero(t, repo.cleanupDedupCalls)
}

func TestRuntimeLogConfig_RequestRetentionCompatibility(t *testing.T) {
	settings := newRuntimeSettingRepoStub()
	settings.values[SettingKeyOpsRuntimeLogConfig] = `{"level":"info","retention_days":7,"request_retention_days":60}`
	svc := &OpsService{settingRepo: settings}
	require.NoError(t, logger.Init(logger.InitOptions{Level: "info", Format: "json", Output: logger.OutputOptions{ToStdout: true}}))
	t.Cleanup(logger.Sync)
	legacy := defaultOpsRuntimeLogConfig(nil)
	legacy.RequestRetentionDays = nil
	updated, err := svc.UpdateRuntimeLogConfig(context.Background(), legacy, 1)
	require.NoError(t, err)
	require.Equal(t, 60, *updated.RequestRetentionDays)
	for _, days := range []int{1, 90} {
		updated.RequestRetentionDays = &days
		saved, err := svc.UpdateRuntimeLogConfig(context.Background(), updated, 1)
		require.NoError(t, err)
		require.Equal(t, days, *saved.RequestRetentionDays)
		var persisted OpsRuntimeLogConfig
		require.NoError(t, json.Unmarshal([]byte(settings.values[SettingKeyOpsRuntimeLogConfig]), &persisted))
		require.Equal(t, days, *persisted.RequestRetentionDays)
	}
	for _, days := range []int{-1, 0, 91, 3650} {
		updated.RequestRetentionDays = &days
		before := settings.values[SettingKeyOpsRuntimeLogConfig]
		_, err := svc.UpdateRuntimeLogConfig(context.Background(), updated, 1)
		require.ErrorContains(t, err, "request_retention_days")
		require.Equal(t, before, settings.values[SettingKeyOpsRuntimeLogConfig])
	}
	reset, err := svc.ResetRuntimeLogConfig(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 90, *reset.RequestRetentionDays)
}
