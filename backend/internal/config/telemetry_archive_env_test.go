package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestTelemetryArchiveDefaultsOffAndBindsEnvironment(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.TelemetryArchive.Enabled)
	require.Empty(t, cfg.TelemetryArchive.Region)
	require.Empty(t, cfg.TelemetryArchive.Bucket)
	require.Equal(t, "prod/raw-telemetry", cfg.TelemetryArchive.Prefix)
	require.Equal(t, 8192, cfg.TelemetryArchive.QueueSize)
	require.Equal(t, int64(32*1024*1024), cfg.TelemetryArchive.QueueMaxBytes)
	require.Equal(t, 1024*1024, cfg.TelemetryArchive.MaxEventBytes)
	require.Equal(t, 256, cfg.TelemetryArchive.BatchSize)
	require.Equal(t, 4, cfg.TelemetryArchive.WorkerCount)
	require.Equal(t, 5, cfg.TelemetryArchive.FlushIntervalSeconds)
	require.Equal(t, 10, cfg.TelemetryArchive.PutTimeoutSeconds)

	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("TELEMETRY_ARCHIVE_ENABLED", "true")
	t.Setenv("TELEMETRY_ARCHIVE_REGION", "us-east-1")
	t.Setenv("TELEMETRY_ARCHIVE_BUCKET", "tokenkey-prod-archive")
	t.Setenv("TELEMETRY_ARCHIVE_PREFIX", "prod/raw-telemetry")
	cfg, err = Load()
	require.NoError(t, err)
	require.True(t, cfg.TelemetryArchive.Enabled)
	require.Equal(t, "us-east-1", cfg.TelemetryArchive.Region)
	require.Equal(t, "tokenkey-prod-archive", cfg.TelemetryArchive.Bucket)
	require.Equal(t, "prod/raw-telemetry", cfg.TelemetryArchive.Prefix)
}

func TestTelemetryArchiveEnabledRequiresCompleteBoundedConfig(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("TELEMETRY_ARCHIVE_ENABLED", "true")
	t.Setenv("TELEMETRY_ARCHIVE_REGION", "")
	t.Setenv("TELEMETRY_ARCHIVE_BUCKET", "")
	_, err := Load()
	require.ErrorContains(t, err, "telemetry_archive.region is required when enabled")

	viper.Reset()
	t.Setenv("TELEMETRY_ARCHIVE_REGION", "us-east-1")
	t.Setenv("TELEMETRY_ARCHIVE_BUCKET", "archive")
	t.Setenv("TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES", "1024")
	t.Setenv("TELEMETRY_ARCHIVE_MAX_EVENT_BYTES", "2048")
	_, err = Load()
	require.ErrorContains(t, err, "max_event_bytes")
}

func TestTelemetryArchiveDisabledIgnoresDormantInvalidLimits(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("TELEMETRY_ARCHIVE_ENABLED", "false")
	t.Setenv("TELEMETRY_ARCHIVE_QUEUE_SIZE", "0")
	t.Setenv("TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES", "1")
	t.Setenv("TELEMETRY_ARCHIVE_MAX_EVENT_BYTES", "2")
	_, err := Load()
	require.NoError(t, err)
}
