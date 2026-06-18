package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// Prod enables S3 export_storage via docker-compose env (QA_CAPTURE_EXPORT_STORAGE_*).
// viper.AutomaticEnv only binds nested keys it "knows" (via SetDefault / config file);
// qa_capture.export_storage.* has no SetDefault, so this pins that env injection
// actually reaches cfg.QACapture.ExportStorage — otherwise the S3 cutover silently
// no-ops and the export stays localfs.
func TestQAExportStorageEnvBinding(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("QA_CAPTURE_EXPORT_STORAGE_DRIVER", "s3")
	t.Setenv("QA_CAPTURE_EXPORT_STORAGE_REGION", "us-east-1")
	t.Setenv("QA_CAPTURE_EXPORT_STORAGE_BUCKET", "tk-test-qa-export")
	t.Setenv("QA_CAPTURE_EXPORT_STORAGE_PREFIX", "traj-exports")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	es := cfg.QACapture.ExportStorage
	if es.Driver != "s3" {
		t.Fatalf("export_storage.driver env NOT bound (got %q) — needs viper.SetDefault for the nested keys", es.Driver)
	}
	if es.Region != "us-east-1" || es.Bucket != "tk-test-qa-export" || es.Prefix != "traj-exports" {
		t.Fatalf("export_storage env partially bound: %+v", es)
	}
}

// Backward-compat anchor: with no QA_CAPTURE_EXPORT_STORAGE_* env, the driver
// stays empty so NewService keeps the export ZIP on the localfs capture store
// (the SetDefault must not silently flip any deployment to S3).
func TestQAExportStorageDefaultsToEmptyDriver(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if d := cfg.QACapture.ExportStorage.Driver; d != "" {
		t.Fatalf("export_storage.driver default must stay empty (localfs fallback), got %q", d)
	}
}
