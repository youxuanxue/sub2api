//go:build unit

package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
)

type fakeReconcileControl struct {
	shardID        int64
	pending        []CommitSegment
	imported       int
	started        int
	verified       int
	segmentFailed  int
	committed      *VerifiedCommit
	orphaned       int
	failureCode    string
	failureCtxDone bool
}

func (f *fakeReconcileControl) EnsureShard(context.Context, *sql.Conn, Window) (int64, error) {
	if f.shardID == 0 {
		f.shardID = 1
	}
	return f.shardID, nil
}
func (f *fakeReconcileControl) ImportCommit(context.Context, *sql.Conn, int64, VerifiedCommit) error {
	f.imported++
	return nil
}
func (f *fakeReconcileControl) OrphanIncomplete(context.Context, *sql.Conn, int64) error {
	f.orphaned++
	return nil
}
func (f *fakeReconcileControl) PendingVerified(context.Context, *sql.Conn, int64) ([]CommitSegment, error) {
	return append([]CommitSegment(nil), f.pending...), nil
}
func (f *fakeReconcileControl) StartSegment(context.Context, *sql.Conn, int64, BuiltSegment, string) (int64, error) {
	f.started++
	return int64(100 + f.started), nil
}
func (f *fakeReconcileControl) MarkSegmentVerified(context.Context, *sql.Conn, int64, VerifiedSegment) error {
	f.verified++
	return nil
}
func (f *fakeReconcileControl) FailSegment(context.Context, *sql.Conn, int64, string, error) error {
	f.segmentFailed++
	return nil
}
func (f *fakeReconcileControl) PersistCommit(_ context.Context, _ *sql.Conn, _ int64, commit VerifiedCommit) error {
	f.committed = &commit
	return nil
}
func (f *fakeReconcileControl) Fail(ctx context.Context, _ *sql.Conn, _ int64, code string, _ error) error {
	f.failureCode = code
	f.failureCtxDone = ctx.Err() != nil
	return nil
}

type conflictOnceStore struct {
	*MemoryObjectStore
	conflicts int
}

func (s *conflictOnceStore) CompareAndSwap(ctx context.Context, key, etag string, body io.Reader, size int64, contentType string) (ObjectInfo, error) {
	if s.conflicts == 0 {
		s.conflicts++
		return ObjectInfo{}, ErrPreconditionFailed
	}
	return s.MemoryObjectStore.CompareAndSwap(ctx, key, etag, body, size, contentType)
}

func TestReconcilerLateRowsAppendDeltaAndCASRetry(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	store, baseDescriptor := verifiedSegmentFixture(t)
	baseManifestBytes, _ := store.Get(ctx, baseDescriptor.ManifestKey)
	var baseManifest SegmentManifest
	_ = json.Unmarshal(baseManifestBytes, &baseManifest)
	legacyCommit := CommitDocument{
		SchemaVersion: CommitSchemaV1, WindowStart: start, WindowEnd: start.Add(time.Hour),
		Segments:        []CommitSegment{{SegmentID: baseManifest.SegmentID, SegmentKind: SegmentKindBase, ManifestKey: baseDescriptor.ManifestKey, ManifestSHA256: baseDescriptor.ManifestSHA256}},
		AggregateSHA256: baseDescriptor.ManifestSHA256, CommittedAt: start.Add(20 * time.Minute),
	}
	legacyBytes, _ := MarshalJSON(legacyCommit)
	commitKey := ShardRelativePrefix(start) + "/commit.json"
	_ = store.Put(ctx, commitKey, legacyBytes, "application/json")
	conflictStore := &conflictOnceStore{MemoryObjectStore: store}

	built := builtDeltaFixture(t, start)
	control := &fakeReconcileControl{}
	reconciler := NewReconciler(conflictStore, control, t.TempDir())
	reconciler.Build = func(context.Context, *sql.Conn, BuildInput) (BuiltSegment, error) { return built, nil }

	receipt, err := reconciler.Reconcile(ctx, nil, Window{Start: start, End: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Reconcile()=%v", err)
	}
	if conflictStore.conflicts != 1 || !receipt.Uploaded || receipt.SegmentCount != 2 || receipt.RecordCount != 2 || receipt.DeletionAuthorized {
		t.Fatalf("receipt=%+v conflicts=%d", receipt, conflictStore.conflicts)
	}
	if control.imported != 1 || control.started != 1 || control.verified != 1 || control.committed == nil {
		t.Fatalf("control=%+v", control)
	}
	commitBytes, _ := store.Get(ctx, commitKey)
	var committed CommitDocument
	_ = json.Unmarshal(commitBytes, &committed)
	if committed.SchemaVersion != CommitSchemaV2 || committed.Segments[0].SegmentKind != SegmentKindBase || committed.Segments[1].SegmentKind != SegmentKindDelta {
		t.Fatalf("commit=%+v", committed)
	}
}

func TestReconcilerNoDeltaCreatesNoSecondBase(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	store, descriptor := verifiedSegmentFixture(t)
	manifestBytes, _ := store.Get(ctx, descriptor.ManifestKey)
	var manifest SegmentManifest
	_ = json.Unmarshal(manifestBytes, &manifest)
	commit := CommitDocument{
		SchemaVersion: CommitSchemaV1, WindowStart: start, WindowEnd: start.Add(time.Hour),
		Segments:        []CommitSegment{{SegmentID: manifest.SegmentID, SegmentKind: SegmentKindBase, ManifestKey: descriptor.ManifestKey, ManifestSHA256: descriptor.ManifestSHA256}},
		AggregateSHA256: descriptor.ManifestSHA256, CommittedAt: time.Now().UTC(),
	}
	body, _ := MarshalJSON(commit)
	commitKey := ShardRelativePrefix(start) + "/commit.json"
	_ = store.Put(ctx, commitKey, body, "application/json")
	before := len(store.Keys())

	control := &fakeReconcileControl{}
	reconciler := NewReconciler(store, control, t.TempDir())
	reconciler.Build = func(context.Context, *sql.Conn, BuildInput) (BuiltSegment, error) {
		return emptyBuiltSegment(t, start, SegmentKindDelta), nil
	}
	receipt, err := reconciler.Reconcile(ctx, nil, Window{Start: start, End: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Reconcile()=%v", err)
	}
	if len(store.Keys()) != before || receipt.Uploaded || receipt.SegmentCount != 1 || control.started != 0 {
		t.Fatalf("receipt=%+v before=%d after=%d control=%+v", receipt, before, len(store.Keys()), control)
	}
}

func TestUS045_ReconcilerTimelyZeroRowCommitsRestorableBase(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC)
	store := NewMemoryObjectStore()
	control := &fakeReconcileControl{}
	reconciler := NewReconciler(store, control, t.TempDir())
	reconciler.Now = func() time.Time { return start.Add(2 * time.Hour) }
	reconciler.Build = func(context.Context, *sql.Conn, BuildInput) (BuiltSegment, error) {
		return zeroBuiltSegmentFixture(t, start, SegmentKindBase, "empty-base"), nil
	}

	receipt, err := reconciler.Reconcile(ctx, nil, Window{Start: start, End: start.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Uploaded || receipt.SegmentCount != 1 || receipt.RecordCount != 0 || control.started != 1 || control.verified != 1 || control.committed == nil {
		t.Fatalf("receipt=%+v control=%+v", receipt, control)
	}
	restoreDir := filepath.Join(t.TempDir(), "restore")
	verified, err := VerifyCommit(ctx, store, ShardRelativePrefix(start)+"/commit.json", restoreDir)
	if err != nil {
		t.Fatalf("restore empty base: %v", err)
	}
	defer func() { _ = verified.Close() }()
	if verified.RecordCount != 0 || len(verified.Document.Segments) != 1 {
		t.Fatalf("verified=%+v", verified)
	}
	if _, err := os.Stat(filepath.Join(restoreDir, "segments", "empty-base", "records.parquet")); err != nil {
		t.Fatalf("restored empty parquet: %v", err)
	}
}

func TestUS045_ReconcilerExpiredZeroRowBecomesSourceUnavailableFailure(t *testing.T) {
	start := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	store := NewMemoryObjectStore()
	control := &fakeReconcileControl{}
	reconciler := NewReconciler(store, control, t.TempDir())
	reconciler.Now = func() time.Time { return start.Add(25 * time.Hour) }
	reconciler.Build = func(context.Context, *sql.Conn, BuildInput) (BuiltSegment, error) {
		return zeroBuiltSegmentFixture(t, start, SegmentKindBase, "expired-empty"), nil
	}

	_, err := reconciler.Reconcile(context.Background(), nil, Window{Start: start, End: start.Add(time.Hour)})
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || integrity.Code != IntegritySourceUnavailableAfterRetention {
		t.Fatalf("err=%v", err)
	}
	if control.failureCode != IntegritySourceUnavailableAfterRetention || control.started != 0 || len(store.Keys()) != 0 {
		t.Fatalf("control=%+v keys=%v", control, store.Keys())
	}
}

func TestReconcilerVerificationFailureMarksSegmentFailed(t *testing.T) {
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	control := &fakeReconcileControl{}
	reconciler := NewReconciler(NewMemoryObjectStore(), control, t.TempDir())
	reconciler.Build = func(context.Context, *sql.Conn, BuildInput) (BuiltSegment, error) {
		return builtSegmentFixture(t, start, SegmentKindBase, "base-failed", "req-failed"), nil
	}
	reconciler.VerifyOne = func(context.Context, ObjectStore, SegmentDescriptor, string) (VerifiedSegment, error) {
		return VerifiedSegment{}, &IntegrityError{Code: IntegrityCorruptArtifact, Err: errors.New("checksum mismatch")}
	}

	_, err := reconciler.Reconcile(context.Background(), nil, Window{Start: start, End: start.Add(time.Hour)})
	if err == nil || control.started != 1 || control.segmentFailed != 1 || control.verified != 0 {
		t.Fatalf("err=%v control=%+v", err, control)
	}
}

func TestReconcilerFailurePersistenceUsesLiveContextAndOrphansInterruptedSegments(t *testing.T) {
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	control := &fakeReconcileControl{}
	reconciler := NewReconciler(NewMemoryObjectStore(), control, t.TempDir())
	reconciler.Build = func(context.Context, *sql.Conn, BuildInput) (BuiltSegment, error) {
		return BuiltSegment{}, context.DeadlineExceeded
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reconciler.Reconcile(ctx, nil, Window{Start: start, End: start.Add(time.Hour)})
	if !errors.Is(err, context.DeadlineExceeded) || control.orphaned != 1 || control.failureCtxDone {
		t.Fatalf("err=%v control=%+v", err, control)
	}
}

func TestReconcilerMissingEvidenceBlocksShardBeforeUpload(t *testing.T) {
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	store := NewMemoryObjectStore()
	control := &fakeReconcileControl{}
	reconciler := NewReconciler(store, control, t.TempDir())
	reconciler.Build = func(context.Context, *sql.Conn, BuildInput) (BuiltSegment, error) {
		return BuiltSegment{}, &IntegrityError{Code: IntegrityMissingEvidence, RequestID: "safe-id", Err: errors.New("missing")}
	}

	_, err := reconciler.Reconcile(context.Background(), nil, Window{Start: start, End: start.Add(time.Hour)})
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || control.failureCode != IntegrityMissingEvidence || len(store.Keys()) != 0 {
		t.Fatalf("err=%v control=%+v keys=%v", err, control, store.Keys())
	}
}

func TestReconcilerResumesVerifiedSegmentWithoutRebuilding(t *testing.T) {
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	store := NewMemoryObjectStore()
	built := builtSegmentFixture(t, start, SegmentKindBase, "base-pending", "req-pending")
	uploadBuiltFixture(t, store, built)
	descriptor := CommitSegment{
		SegmentID: built.SegmentID, SegmentKind: SegmentKindBase,
		ManifestKey:    ShardRelativePrefix(start) + "/segments/" + built.SegmentID + "/manifest.json",
		ManifestSHA256: built.Artifacts[len(built.Artifacts)-1].SHA256,
	}
	control := &fakeReconcileControl{pending: []CommitSegment{descriptor}}
	reconciler := NewReconciler(store, control, t.TempDir())
	reconciler.Build = func(context.Context, *sql.Conn, BuildInput) (BuiltSegment, error) {
		t.Fatal("verified pending segment should be reused")
		return BuiltSegment{}, nil
	}

	receipt, err := reconciler.Reconcile(context.Background(), nil, Window{Start: start, End: start.Add(time.Hour)})
	if err != nil {
		t.Fatalf("Reconcile()=%v", err)
	}
	if receipt.SegmentCount != 1 || receipt.RecordCount != 1 || control.committed == nil {
		t.Fatalf("receipt=%+v control=%+v", receipt, control)
	}
}

func builtDeltaFixture(t *testing.T, start time.Time) BuiltSegment {
	t.Helper()
	return builtSegmentFixture(t, start, SegmentKindDelta, "delta-1", "req-2")
}

func builtSegmentFixture(t *testing.T, start time.Time, kind, segmentID, requestID string) BuiltSegment {
	t.Helper()
	blobURI := "file:///source/request.json.zst"
	row := RecordRow{
		RequestID: requestID, UserID: 1, APIKeyID: 2, Platform: "anthropic",
		RequestedModel: "claude", StatusCode: 200, Success: true,
		RequestSHA256: "a", ResponseSHA256: "b", CaptureStatus: "captured",
		RequestBlobURI: &blobURI, CreatedAt: start.Add(3 * time.Minute).UnixMicro(),
	}
	var records bytes.Buffer
	parquetWriter := parquet.NewGenericWriter[RecordRow](&records)
	if _, err := parquetWriter.Write([]RecordRow{row}); err != nil {
		t.Fatal(err)
	}
	if err := parquetWriter.Close(); err != nil {
		t.Fatal(err)
	}
	evidence := []byte("evidence-" + requestID)
	line := evidenceIndexLine{
		RequestID: requestID, BlobField: "request_blob_uri", BlobURI: blobURI,
		Offset: 0, Length: int64(len(evidence)), SHA256: SHA256Hex(evidence),
	}
	lineJSON, _ := json.Marshal(line)
	var index bytes.Buffer
	zstdWriter, err := zstd.NewWriter(&index)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = zstdWriter.Write(append(lineJSON, '\n'))
	if err := zstdWriter.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := SegmentManifest{
		SchemaVersion: ManifestSchemaV1, SegmentID: segmentID, SegmentKind: kind,
		WindowStart: start, WindowEnd: start.Add(time.Hour), RecordCount: 1,
		BlobRefCount: 1, BlobPresentCount: 1,
		ArtifactBytes: int64(records.Len() + len(evidence) + index.Len()),
		RecordsSHA256: SHA256Hex(records.Bytes()), EvidencePackSHA256: SHA256Hex(evidence),
		EvidenceIndexSHA256: SHA256Hex(index.Bytes()),
	}
	root := t.TempDir()
	var artifacts []BuiltArtifact
	for name, body := range map[string][]byte{
		"records.parquet": records.Bytes(), "evidence.pack": evidence,
		"evidence-index.jsonl.zst": index.Bytes(),
	} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, BuiltArtifact{Name: name, Path: path, Size: int64(len(body)), SHA256: SHA256Hex(body), ContentType: "application/octet-stream"})
	}
	manifestBytes, _ := MarshalJSON(manifest)
	manifestPath := filepath.Join(root, "manifest.json")
	_ = os.WriteFile(manifestPath, manifestBytes, 0o600)
	artifacts = append(artifacts, BuiltArtifact{Name: "manifest.json", Path: manifestPath, Size: int64(len(manifestBytes)), SHA256: SHA256Hex(manifestBytes), ContentType: "application/json"})
	return BuiltSegment{SegmentID: segmentID, ScratchDir: root, Manifest: manifest, Artifacts: artifacts}
}

func emptyBuiltSegment(t *testing.T, start time.Time, kind string) BuiltSegment {
	t.Helper()
	root := t.TempDir()
	return BuiltSegment{SegmentID: "empty", ScratchDir: root, Manifest: SegmentManifest{
		SchemaVersion: ManifestSchemaV1, SegmentID: "empty", SegmentKind: kind,
		WindowStart: start, WindowEnd: start.Add(time.Hour),
	}}
}

func zeroBuiltSegmentFixture(t *testing.T, start time.Time, kind, segmentID string) BuiltSegment {
	t.Helper()
	root := t.TempDir()
	var records bytes.Buffer
	writer := parquet.NewGenericWriter[RecordRow](&records)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	recordsPath := filepath.Join(root, "records.parquet")
	if err := os.WriteFile(recordsPath, records.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := SegmentManifest{
		SchemaVersion: ManifestSchemaV1, SegmentID: segmentID, SegmentKind: kind,
		WindowStart: start, WindowEnd: start.Add(time.Hour), ArtifactBytes: int64(records.Len()),
		RecordsSHA256: SHA256Hex(records.Bytes()),
	}
	manifestBytes, err := MarshalJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return BuiltSegment{
		SegmentID: segmentID, ScratchDir: root, RecordsPath: recordsPath, Manifest: manifest,
		Artifacts: []BuiltArtifact{
			{Name: "records.parquet", Path: recordsPath, Size: int64(records.Len()), SHA256: SHA256Hex(records.Bytes()), ContentType: "application/vnd.apache.parquet"},
			{Name: "manifest.json", Path: manifestPath, Size: int64(len(manifestBytes)), SHA256: SHA256Hex(manifestBytes), ContentType: "application/json"},
		},
	}
}

func uploadBuiltFixture(t *testing.T, store ObjectStore, built BuiltSegment) {
	t.Helper()
	prefix := ShardRelativePrefix(built.Manifest.WindowStart) + "/segments/" + built.SegmentID
	for _, artifact := range built.Artifacts {
		body, err := os.ReadFile(artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(context.Background(), prefix+"/"+artifact.Name, bytes.NewReader(body), int64(len(body)), artifact.ContentType); err != nil {
			t.Fatal(err)
		}
	}
}
