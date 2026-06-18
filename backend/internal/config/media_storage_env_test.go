package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// Prod enables generated-media S3 offload via env (MEDIA_STORAGE_*). viper
// AutomaticEnv only binds nested keys it "knows" (via SetDefault), so this pins
// that the env injection actually reaches cfg.MediaStorage — otherwise the
// offload silently no-ops and video keeps streaming inline base64. Mirrors
// TestQAExportStorageEnvBinding.
func TestMediaStorageEnvBinding(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("MEDIA_STORAGE_DRIVER", "s3")
	t.Setenv("MEDIA_STORAGE_REGION", "us-east-1")
	t.Setenv("MEDIA_STORAGE_BUCKET", "tk-test-media")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	ms := cfg.MediaStorage
	if ms.Driver != "s3" {
		t.Fatalf("media_storage.driver env NOT bound (got %q) — needs viper.SetDefault for the nested keys", ms.Driver)
	}
	if ms.Region != "us-east-1" || ms.Bucket != "tk-test-media" {
		t.Fatalf("media_storage env partially bound: %+v", ms)
	}
	// Credentials stay empty → the store uses the default chain (instance role).
	if ms.AccessKeyID != "" || ms.SecretAccessKey != "" {
		t.Fatalf("media_storage creds must default empty, got %+v", ms)
	}
}

// Backward-compat anchor: with no MEDIA_STORAGE_* env the driver stays empty so
// the gateway keeps passing media through as inline base64 (no surprise S3 deps).
func TestMediaStorageDefaultsToEmptyDriver(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if d := cfg.MediaStorage.Driver; d != "" {
		t.Fatalf("media_storage.driver default must stay empty (passthrough), got %q", d)
	}
}
