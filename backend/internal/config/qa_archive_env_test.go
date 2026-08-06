//go:build unit

package config

import "testing"

func TestValidateQaArchiveDisabledAllowsEmptyStorage(t *testing.T) {
	if err := validateQaArchiveConfig(QaArchiveConfig{}); err != nil {
		t.Fatalf("validateQaArchiveConfig()=%v", err)
	}
}

func TestValidateQaArchiveEnabledRequiresS3Bucket(t *testing.T) {
	err := validateQaArchiveConfig(QaArchiveConfig{
		Enabled: true,
		Storage: QACaptureStorageConfig{
			Driver: "s3",
			Region: "us-east-1",
			Bucket: "tokenkey-prod-qa-raw-archive-123",
			Prefix: "raw/v1",
		},
	})
	if err != nil {
		t.Fatalf("validateQaArchiveConfig()=%v", err)
	}

	err = validateQaArchiveConfig(QaArchiveConfig{
		Enabled: true,
		Storage: QACaptureStorageConfig{Driver: "localfs"},
	})
	if err == nil {
		t.Fatal("expected error for non-s3 driver")
	}
}
