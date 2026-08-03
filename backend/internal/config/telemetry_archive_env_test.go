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
