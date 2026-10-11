//go:build unit

package config

import "testing"

func TestQABundleEnvBinding(t *testing.T) {
	t.Setenv("QA_BUNDLE_ENABLED", "true")
	t.Setenv("QA_BUNDLE_QUEUE_URL", "https://sqs.us-east-1.amazonaws.com/123/qa-bundle")
	t.Setenv("QA_BUNDLE_STORAGE_DRIVER", "s3")
	t.Setenv("QA_BUNDLE_STORAGE_REGION", "us-east-1")
	t.Setenv("QA_BUNDLE_STORAGE_BUCKET", "qa-bundles")
	t.Setenv("QA_BUNDLE_STORAGE_PREFIX", "user-qa")
	t.Setenv("QA_BUNDLE_STORAGE_CREDENTIALS_FILE", "/run/secrets/tokenkey-qa/bundle.json")
	t.Setenv("QA_ARCHIVE_STORAGE_CREDENTIALS_FILE", "/run/secrets/tokenkey-qa/raw.json")

	cfg, err := LoadForBootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QaBundle.Storage.CredentialsFile != "/run/secrets/tokenkey-qa/bundle.json" ||
		cfg.QaArchive.Storage.CredentialsFile != "/run/secrets/tokenkey-qa/raw.json" {
		t.Fatal("QA file credentials not bound")
	}
	if !cfg.QaBundle.Enabled || cfg.QaBundle.QueueURL == "" || cfg.QaBundle.Storage.Driver != "s3" ||
		cfg.QaBundle.Storage.Region != "us-east-1" || cfg.QaBundle.Storage.Bucket != "qa-bundles" || cfg.QaBundle.Storage.Prefix != "user-qa" {
		t.Fatalf("qa_bundle=%+v", cfg.QaBundle)
	}
}

func TestQABundlePostgresConfigRejectsAmbiguousDriver(t *testing.T) {
	valid := QaBundleConfig{Enabled: true, QueueDriver: "postgres", QueuePostgresDSNFile: "/run/secrets/queue.dsn",
		Storage: QACaptureStorageConfig{Driver: "s3", Region: "fsn1", Bucket: "qa", Prefix: "bundle"}}
	if err := validateQaBundleConfig(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*QaBundleConfig){
		func(c *QaBundleConfig) { c.QueueURL = "https://sqs.example/queue" },
		func(c *QaBundleConfig) { c.QueuePostgresDSNFile = "" },
		func(c *QaBundleConfig) { c.QueueDriver = "unknown" },
		func(c *QaBundleConfig) { c.QueueDriver = "sqs"; c.QueueURL = "https://sqs.example/queue" },
	} {
		cfg := valid
		mutate(&cfg)
		if err := validateQaBundleConfig(cfg); err == nil {
			t.Fatal("ambiguous or incomplete queue accepted")
		}
	}
	t.Setenv("QA_BUNDLE_QUEUE_DRIVER", "postgres")
	t.Setenv("QA_BUNDLE_QUEUE_POSTGRES_DSN_FILE", "/run/secrets/queue.dsn")
	t.Setenv("QA_BUNDLE_QUEUE_URL", "")
	cfg, err := LoadForBootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QaBundle.QueueDriver != "postgres" || cfg.QaBundle.QueuePostgresDSNFile != "/run/secrets/queue.dsn" {
		t.Fatal("PostgreSQL queue environment not bound")
	}
}
