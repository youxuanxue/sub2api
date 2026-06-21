//go:build unit

package qa

import (
	"context"
	"strings"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// On prod the QA export S3 store authenticates via the EC2 instance role: the
// deploy injects only DRIVER/REGION/BUCKET/PREFIX and the QaExports bucket
// policy grants the instance role Put/Get/Delete. Forcing an empty static
// credentials provider broke every export with "static credentials are empty"
// (PutObject). This locks the fix: a static provider is added ONLY when an
// access key is configured; otherwise we fall through to the default chain.
func TestQAS3LoadOptions_StaticCredsOnlyWhenConfigured(t *testing.T) {
	ctx := context.Background()

	// No access key (the prod / instance-role path): exactly one option (region),
	// no static credentials provider injected.
	noCreds := qaS3LoadOptions(config.QACaptureStorageConfig{
		Driver: "s3", Region: "us-east-1", Bucket: "tokenkey-prod-qa-exports",
	})
	if len(noCreds) != 1 {
		t.Fatalf("empty access key must add only the region option (no static provider); got %d options", len(noCreds))
	}

	// Explicit access key (R2 / MinIO / local S3): region + static provider, and
	// the resolved credentials are exactly the configured static pair.
	withCreds := qaS3LoadOptions(config.QACaptureStorageConfig{
		Driver: "s3", Region: "auto", Bucket: "b",
		AccessKeyID: "AKIDTEST", SecretAccessKey: "SECRETTEST",
	})
	if len(withCreds) != 2 {
		t.Fatalf("configured access key must add a static credentials provider; got %d options", len(withCreds))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, withCreds...)
	if err != nil {
		t.Fatalf("LoadDefaultConfig with static creds: %v", err)
	}
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		t.Fatalf("retrieve static creds: %v", err)
	}
	if creds.AccessKeyID != "AKIDTEST" || creds.SecretAccessKey != "SECRETTEST" {
		t.Fatalf("static creds not honored: got %q/%q", creds.AccessKeyID, "<redacted>")
	}
}

// The regression guard, stated as the actual symptom: building the config the
// empty-access-key way and resolving credentials must NOT yield the
// "static credentials are empty" error that the empty static provider produced.
// (It may fail with a default-chain error in a credential-less CI sandbox, or
// succeed if ambient creds exist — either is fine; only the empty-static error
// is the regression.)
func TestQAS3LoadOptions_EmptyKeyDoesNotForceEmptyStatic(t *testing.T) {
	// Keep the default chain from probing IMDS so the test never hangs/network-calls.
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	ctx := context.Background()

	cfg, err := awsconfig.LoadDefaultConfig(ctx, qaS3LoadOptions(config.QACaptureStorageConfig{
		Driver: "s3", Region: "us-east-1", Bucket: "b",
	})...)
	if err != nil {
		t.Fatalf("LoadDefaultConfig (no creds): %v", err)
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		if strings.Contains(err.Error(), "static credentials are empty") {
			t.Fatalf("regression: empty static provider forced — export would fail on prod: %v", err)
		}
		// Any other error (no creds available in sandbox) is acceptable here.
	}
}
