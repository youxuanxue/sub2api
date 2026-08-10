//go:build unit

package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestQaArchiveEnvBinding(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	t.Setenv("QA_ARCHIVE_ENABLED", "true")
	t.Setenv("QA_ARCHIVE_STORAGE_DRIVER", "s3")
	t.Setenv("QA_ARCHIVE_STORAGE_REGION", "us-east-1")
	t.Setenv("QA_ARCHIVE_STORAGE_BUCKET", "tokenkey-prod-qa-raw-archive-123")
	t.Setenv("QA_ARCHIVE_STORAGE_PREFIX", "raw/v1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load()=%v", err)
	}
	if !cfg.QaArchive.Enabled {
		t.Fatal("qa_archive.enabled not bound from QA_ARCHIVE_ENABLED")
	}
	st := cfg.QaArchive.Storage
	if st.Driver != "s3" || st.Region != "us-east-1" || st.Bucket != "tokenkey-prod-qa-raw-archive-123" || st.Prefix != "raw/v1" {
		t.Fatalf("qa_archive.storage env partially bound: %+v", st)
	}
}

func TestQaArchiveDefaultsDisabled(t *testing.T) {
	viper.Reset()
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load()=%v", err)
	}
	if cfg.QaArchive.Enabled {
		t.Fatal("qa_archive.enabled must default false")
	}
}
