//go:build unit

package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
)

func TestBundleJobSpecRequiresExactlyTwentyFourContiguousCommits(t *testing.T) {
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	spec := NewBundleJobSpec(7, 11, watermark)
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(spec.CommitKeys) != 24 || spec.DataFrom != watermark.Add(-24*time.Hour) || spec.DataUntil != watermark {
		t.Fatalf("spec=%+v", spec)
	}

	missing := spec
	missing.CommitKeys = missing.CommitKeys[:23]
	if err := missing.Validate(); err == nil {
		t.Fatal("Validate() accepted 23 commits")
	}
	foreign := spec
	foreign.CommitKeys = append([]string(nil), spec.CommitKeys...)
	foreign.CommitKeys[3] = "../foreign/commit.json"
	if err := foreign.Validate(); err == nil {
		t.Fatal("Validate() accepted a traversal commit key")
	}
}

func TestExecuteBundleJobVerifiesEveryCommittedHourBeforePublishing(t *testing.T) {
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	spec := NewBundleJobSpec(7, 11, watermark)
	store := &recordingStore{}
	verified := 0

	receipt, err := ExecuteJob(context.Background(), spec, panicReadOnlyStore{}, store, ExecuteDeps{
		RestoreRoot: t.TempDir(),
		VerifyCommit: func(_ context.Context, _ archive.ReadOnlyObjectStore, key, _ string) (archive.VerifiedCommit, error) {
			expected := spec.DataFrom.Add(time.Duration(verified) * time.Hour)
			if key != archive.ShardRelativePrefix(expected)+"/commit.json" {
				return archive.VerifiedCommit{}, fmt.Errorf("unexpected key %s", key)
			}
			verified++
			return archive.VerifiedCommit{Document: archive.CommitDocument{WindowStart: expected, WindowEnd: expected.Add(time.Hour)}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if verified != 24 || receipt.Kind != JobKindBundle || receipt.ManifestKey != spec.ManifestKey || receipt.RecordCount != 0 {
		t.Fatalf("verified=%d receipt=%+v", verified, receipt)
	}
	if _, ok := store.objects[spec.ManifestKey]; !ok {
		t.Fatalf("manifest %s was not published", spec.ManifestKey)
	}
	if _, ok := store.objects[spec.ReceiptKey]; !ok {
		t.Fatalf("receipt %s was not published", spec.ReceiptKey)
	}
}

func TestExecuteZipJobDoesNotAccessRawArchive(t *testing.T) {
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	bundleSpec := NewBundleJobSpec(7, 11, watermark)
	store := &recordingStore{}
	manifest, err := Publish(context.Background(), store, PublishInput{
		Prefix: bundleSpec.GenerationPrefix, DataFrom: bundleSpec.DataFrom,
		DataUntil: bundleSpec.DataUntil, ArchiveWatermark: bundleSpec.ArchiveWatermark,
	})
	if err != nil {
		t.Fatal(err)
	}
	zipSpec := NewZipJobSpec(bundleSpec, manifest.ManifestKey)
	receipt, err := ExecuteJob(context.Background(), zipSpec, panicReadOnlyStore{}, store, ExecuteDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Kind != JobKindBundleZip || receipt.StorageKey != zipSpec.OutputKey {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func TestZipJobSpecRejectsForeignManifest(t *testing.T) {
	watermark := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	bundleSpec := NewBundleJobSpec(7, 11, watermark)
	zipSpec := NewZipJobSpec(bundleSpec, bundleSpec.ManifestKey)
	zipSpec.ManifestKey = "qa-bundles/v1/jobs/" + strings.Repeat("f", 64) + "/generation/manifest.json"
	if err := zipSpec.Validate(); err == nil {
		t.Fatal("Validate() accepted a foreign bundle manifest")
	}
}

func TestJobSpecJSONRoundTripRestoresDerivedControlKeys(t *testing.T) {
	spec := NewBundleJobSpec(7, 11, time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC))
	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseJobSpec(body)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SpecKey != spec.SpecKey || parsed.ReceiptKey != spec.ReceiptKey || parsed.FailureKey != spec.FailureKey {
		t.Fatalf("parsed keys=%s %s %s", parsed.SpecKey, parsed.ReceiptKey, parsed.FailureKey)
	}
}

type panicReadOnlyStore struct{}

func (panicReadOnlyStore) Open(context.Context, string) (archive.ObjectReader, error) {
	panic("zip job accessed raw archive")
}

func (panicReadOnlyStore) HeadInfo(context.Context, string) (archive.ObjectInfo, error) {
	panic("zip job accessed raw archive")
}

func (panicReadOnlyStore) Head(context.Context, string) (bool, error) {
	panic("zip job accessed raw archive")
}
