//go:build unit

package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
)

func TestVerifySegmentRestoresEvidenceFromObjectStore(t *testing.T) {
	store, descriptor := verifiedSegmentFixture(t)
	restoreRoot := filepath.Join(t.TempDir(), "restore")

	verified, err := VerifySegment(context.Background(), store, descriptor, restoreRoot)
	if err != nil {
		t.Fatalf("VerifySegment()=%v", err)
	}
	defer func() { _ = verified.Close() }()
	if verified.Manifest.RecordCount != 1 || verified.Manifest.BlobPresentCount != 1 || verified.IdentityCount != 1 {
		t.Fatalf("verified=%+v", verified)
	}
	identityInfo, err := os.Stat(verified.IdentityPath)
	if err != nil || identityInfo.Mode().Perm() != 0o600 {
		t.Fatalf("identity file mode=%v err=%v", identityInfo.Mode().Perm(), err)
	}
	body, err := os.ReadFile(filepath.Join(restoreRoot, "evidence", "req-1", "request_blob_uri.bin"))
	if err != nil || string(body) != "evidence-body" {
		t.Fatalf("restored body=%q err=%v", body, err)
	}
	info, err := os.Stat(filepath.Join(restoreRoot, "evidence", "req-1", "request_blob_uri.bin"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restored mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestVerifySegmentRejectsMissingEvidenceDeclaration(t *testing.T) {
	store, descriptor := verifiedSegmentFixture(t)
	manifestBytes, err := store.Get(context.Background(), descriptor.ManifestKey)
	if err != nil {
		t.Fatal(err)
	}
	var manifest SegmentManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.BlobMissingCount = 1
	manifestBytes, _ = MarshalJSON(manifest)
	if err := store.Put(context.Background(), descriptor.ManifestKey, manifestBytes, "application/json"); err != nil {
		t.Fatal(err)
	}
	descriptor.ManifestSHA256 = SHA256Hex(manifestBytes)

	_, err = VerifySegment(context.Background(), store, descriptor, "")
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || integrity.Code != IntegrityMissingEvidence {
		t.Fatalf("VerifySegment() error=%v", err)
	}
}

func TestVerifySegmentRejectsCorruptEvidencePack(t *testing.T) {
	store, descriptor := verifiedSegmentFixture(t)
	if err := store.Put(context.Background(), descriptor.Prefix+"/evidence.pack", []byte("corrupt"), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}

	_, err := VerifySegment(context.Background(), store, descriptor, "")
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || integrity.Code != IntegrityCorruptArtifact {
		t.Fatalf("VerifySegment() error=%v", err)
	}
}

func TestVerifySegmentRejectsCorruptEvidenceIndex(t *testing.T) {
	store, descriptor := verifiedSegmentFixture(t)
	if err := store.Put(context.Background(), descriptor.Prefix+"/evidence-index.jsonl.zst", []byte("not-zstd"), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}

	_, err := VerifySegment(context.Background(), store, descriptor, "")
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || integrity.Code != IntegrityCorruptArtifact {
		t.Fatalf("VerifySegment() error=%v", err)
	}
}

func TestVerifyCommitAcceptsLegacySingleBaseAndDerivesAggregate(t *testing.T) {
	store, descriptor := verifiedSegmentFixture(t)
	manifestBytes, err := store.Get(context.Background(), descriptor.ManifestKey)
	if err != nil {
		t.Fatal(err)
	}
	var manifest SegmentManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	commit := CommitDocument{
		SchemaVersion: CommitSchemaV1,
		WindowStart:   manifest.WindowStart,
		WindowEnd:     manifest.WindowEnd,
		Segments: []CommitSegment{{
			SegmentID: manifest.SegmentID, SegmentKind: manifest.SegmentKind,
			ManifestKey: descriptor.ManifestKey, ManifestSHA256: descriptor.ManifestSHA256,
		}},
		AggregateSHA256: descriptor.ManifestSHA256,
		CommittedAt:     time.Now().UTC(),
	}
	commitBytes, _ := MarshalJSON(commit)
	commitKey := "date=2026-08-07/hour=01/commit.json"
	if err := store.Put(context.Background(), commitKey, commitBytes, "application/json"); err != nil {
		t.Fatal(err)
	}

	verified, err := VerifyCommit(context.Background(), store, commitKey, "")
	if err != nil {
		t.Fatalf("VerifyCommit()=%v", err)
	}
	defer func() { _ = verified.Close() }()
	if verified.RecordCount != 1 || verified.BlobRefCount != 1 || len(verified.Segments) != 1 || verified.ETag == "" {
		t.Fatalf("verified=%+v", verified)
	}
}

func TestVerifyCommitRejectsDuplicateIdentityAcrossBaseAndDelta(t *testing.T) {
	store, base := verifiedSegmentFixture(t)
	baseManifestBytes, _ := store.Get(context.Background(), base.ManifestKey)
	var deltaManifest SegmentManifest
	_ = json.Unmarshal(baseManifestBytes, &deltaManifest)
	deltaManifest.SegmentID = "seg-2"
	deltaManifest.SegmentKind = SegmentKindDelta
	deltaManifestBytes, _ := MarshalJSON(deltaManifest)
	deltaPrefix := "date=2026-08-07/hour=01/segments/seg-2"
	for _, name := range []string{"records.parquet", "evidence.pack", "evidence-index.jsonl.zst"} {
		body, _ := store.Get(context.Background(), base.Prefix+"/"+name)
		if err := store.Put(context.Background(), deltaPrefix+"/"+name, body, "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Put(context.Background(), deltaPrefix+"/manifest.json", deltaManifestBytes, "application/json"); err != nil {
		t.Fatal(err)
	}
	segments := []CommitSegment{
		{SegmentID: "seg-1", SegmentKind: SegmentKindBase, ManifestKey: base.ManifestKey, ManifestSHA256: base.ManifestSHA256},
		{SegmentID: "seg-2", SegmentKind: SegmentKindDelta, ManifestKey: deltaPrefix + "/manifest.json", ManifestSHA256: SHA256Hex(deltaManifestBytes)},
	}
	aggregate, _ := CommitAggregateSHA256(segments)
	commit := CommitDocument{
		SchemaVersion: CommitSchemaV2, WindowStart: deltaManifest.WindowStart, WindowEnd: deltaManifest.WindowEnd,
		Segments: segments, AggregateSHA256: aggregate, AggregateRecordCount: 2,
		AggregateBlobRefCount: 2, AggregateBlobPresentCount: 2, CommittedAt: time.Now().UTC(),
	}
	commitBytes, _ := MarshalJSON(commit)
	commitKey := "date=2026-08-07/hour=01/commit.json"
	_ = store.Put(context.Background(), commitKey, commitBytes, "application/json")

	_, err := VerifyCommit(context.Background(), store, commitKey, "")
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || integrity.Code != IntegrityCorruptArtifact {
		t.Fatalf("VerifyCommit() error=%v", err)
	}
}

func TestVerifyCommitV2RejectsAggregateMismatch(t *testing.T) {
	store, descriptor := verifiedSegmentFixture(t)
	manifestBytes, _ := store.Get(context.Background(), descriptor.ManifestKey)
	var manifest SegmentManifest
	_ = json.Unmarshal(manifestBytes, &manifest)
	commit := CommitDocument{
		SchemaVersion: CommitSchemaV2,
		WindowStart:   manifest.WindowStart, WindowEnd: manifest.WindowEnd,
		Segments: []CommitSegment{{
			SegmentID: manifest.SegmentID, SegmentKind: manifest.SegmentKind,
			ManifestKey: descriptor.ManifestKey, ManifestSHA256: descriptor.ManifestSHA256,
		}},
		AggregateSHA256:           descriptor.ManifestSHA256,
		AggregateRecordCount:      2,
		AggregateBlobRefCount:     1,
		AggregateBlobPresentCount: 1,
		CommittedAt:               time.Now().UTC(),
	}
	commitBytes, _ := MarshalJSON(commit)
	if err := store.Put(context.Background(), "date=2026-08-07/hour=01/commit.json", commitBytes, "application/json"); err != nil {
		t.Fatal(err)
	}

	_, err := VerifyCommit(context.Background(), store, "date=2026-08-07/hour=01/commit.json", "")
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || integrity.Code != IntegrityCorruptArtifact {
		t.Fatalf("VerifyCommit() error=%v", err)
	}
}

func verifiedSegmentFixture(t *testing.T) (*MemoryObjectStore, SegmentDescriptor) {
	t.Helper()
	store := NewMemoryObjectStore()
	ctx := context.Background()
	prefix := "date=2026-08-07/hour=01/segments/seg-1"
	createdAt := time.Date(2026, 8, 7, 1, 2, 0, 0, time.UTC)
	blobURI := "file:///source/request.json.zst"
	row := RecordRow{
		RequestID: "req-1", UserID: 1, APIKeyID: 2, Platform: "anthropic",
		RequestedModel: "claude", StatusCode: 200, Success: true,
		RequestSHA256: "a", ResponseSHA256: "b", CaptureStatus: "captured",
		RequestBlobURI: &blobURI, CreatedAt: createdAt.UnixMicro(),
	}
	var parquetBytes bytes.Buffer
	writer := parquet.NewGenericWriter[RecordRow](&parquetBytes)
	if _, err := writer.Write([]RecordRow{row}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	evidence := []byte("evidence-body")
	indexLine := evidenceIndexLine{
		RequestID: "req-1", BlobField: "request_blob_uri", BlobURI: blobURI,
		Offset: 0, Length: int64(len(evidence)), SHA256: SHA256Hex(evidence),
	}
	indexJSON, _ := json.Marshal(indexLine)
	var indexBytes bytes.Buffer
	zw, err := zstd.NewWriter(&indexBytes)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zw.Write(append(indexJSON, '\n'))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := SegmentManifest{
		SchemaVersion: ManifestSchemaV1, SegmentID: "seg-1", SegmentKind: SegmentKindBase,
		WindowStart: createdAt.Truncate(time.Hour), WindowEnd: createdAt.Truncate(time.Hour).Add(time.Hour),
		RecordCount: 1, BlobRefCount: 1, BlobPresentCount: 1,
		ArtifactBytes: int64(parquetBytes.Len() + len(evidence) + indexBytes.Len()),
		RecordsSHA256: SHA256Hex(parquetBytes.Bytes()), EvidencePackSHA256: SHA256Hex(evidence),
		EvidenceIndexSHA256: SHA256Hex(indexBytes.Bytes()),
	}
	manifestBytes, _ := MarshalJSON(manifest)
	for key, body := range map[string][]byte{
		prefix + "/records.parquet":          parquetBytes.Bytes(),
		prefix + "/evidence.pack":            evidence,
		prefix + "/evidence-index.jsonl.zst": indexBytes.Bytes(),
		prefix + "/manifest.json":            manifestBytes,
	} {
		if err := store.Put(ctx, key, body, "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
	}
	return store, SegmentDescriptor{Prefix: prefix, ManifestKey: prefix + "/manifest.json", ManifestSHA256: SHA256Hex(manifestBytes)}
}
