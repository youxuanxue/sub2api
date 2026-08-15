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

	cfg, err := LoadForBootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.QaBundle.Enabled || cfg.QaBundle.QueueURL == "" || cfg.QaBundle.Storage.Driver != "s3" ||
		cfg.QaBundle.Storage.Region != "us-east-1" || cfg.QaBundle.Storage.Bucket != "qa-bundles" || cfg.QaBundle.Storage.Prefix != "user-qa" {
		t.Fatalf("qa_bundle=%+v", cfg.QaBundle)
	}
}
