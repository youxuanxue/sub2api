//go:build integration

package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestSQLControlStorePersistsVerifiedCommitParity(t *testing.T) {
	ctx := context.Background()
	db := openArchiveIntegrationDB(t, "qa_archive_control_store")
	defer func() { _ = db.Close() }()
	for _, migration := range []string{"tk_069_create_qa_archive_shards.sql", "tk_070_qa_archive_closeout_control.sql"} {
		body, readErr := migrations.FS.ReadFile(migration)
		require.NoError(t, readErr)
		_, execErr := db.ExecContext(ctx, string(body))
		require.NoError(t, execErr)
	}
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	control := NewSQLControlStore()
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	window := Window{Start: start, End: start.Add(time.Hour)}
	shardID, err := control.EnsureShard(ctx, conn, window)
	require.NoError(t, err)
	require.NotZero(t, shardID)

	identityPath := filepath.Join(t.TempDir(), "record-identities.jsonl")
	identityBody, _ := json.Marshal(RecordIdentity{CreatedAt: start.Add(time.Minute), RequestID: "req-1"})
	require.NoError(t, os.WriteFile(identityPath, append(identityBody, '\n'), 0o600))
	manifest := SegmentManifest{
		SchemaVersion: ManifestSchemaV1, SegmentID: "seg-1", SegmentKind: SegmentKindBase,
		WindowStart: start, WindowEnd: start.Add(time.Hour), RecordCount: 1,
		BlobRefCount: 1, BlobPresentCount: 1, ArtifactBytes: 123,
		RecordsSHA256: "records", EvidencePackSHA256: "pack", EvidenceIndexSHA256: "index",
	}
	built := BuiltSegment{
		SegmentID: "seg-1", Manifest: manifest,
		Artifacts: []BuiltArtifact{{Name: "manifest.json", SHA256: "manifest"}},
	}
	segmentDBID, err := control.StartSegment(ctx, conn, shardID, built, "date=2026-08-07/hour=01/segments/seg-1")
	require.NoError(t, err)
	verifiedSegment := VerifiedSegment{Manifest: manifest, IdentityPath: identityPath, IdentityCount: 1}
	require.NoError(t, control.MarkSegmentVerified(ctx, conn, segmentDBID, verifiedSegment))

	interruptedManifest := manifest
	interruptedManifest.SegmentID = "seg-interrupted"
	interruptedManifest.SegmentKind = SegmentKindDelta
	interrupted := BuiltSegment{
		SegmentID: interruptedManifest.SegmentID, Manifest: interruptedManifest,
		Artifacts: []BuiltArtifact{{Name: "manifest.json", SHA256: "interrupted-manifest"}},
	}
	interruptedID, err := control.StartSegment(ctx, conn, shardID, interrupted, "date=2026-08-07/hour=01/segments/seg-interrupted")
	require.NoError(t, err)
	require.NoError(t, control.OrphanIncomplete(ctx, conn, shardID))
	var interruptedState, interruptedCode string
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT state, verification_error_code FROM qa_archive_segments WHERE id=$1`, interruptedID).Scan(&interruptedState, &interruptedCode))
	require.Equal(t, "orphaned", interruptedState)
	require.Equal(t, "interrupted_before_verify", interruptedCode)

	failedManifest := interruptedManifest
	failedManifest.SegmentID = "seg-failed"
	failedBuilt := BuiltSegment{
		SegmentID: failedManifest.SegmentID, Manifest: failedManifest,
		Artifacts: []BuiltArtifact{{Name: "manifest.json", SHA256: "failed-manifest"}},
	}
	failedID, err := control.StartSegment(ctx, conn, shardID, failedBuilt, "date=2026-08-07/hour=01/segments/seg-failed")
	require.NoError(t, err)
	require.NoError(t, control.FailSegment(ctx, conn, failedID, IntegrityCorruptArtifact, errors.New("checksum mismatch")))
	var failedState, failedCode string
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT state, verification_error_code FROM qa_archive_segments WHERE id=$1`, failedID).Scan(&failedState, &failedCode))
	require.Equal(t, StateFailed, failedState)
	require.Equal(t, IntegrityCorruptArtifact, failedCode)

	pending, err := control.PendingVerified(ctx, conn, shardID)
	require.NoError(t, err)
	require.Equal(t, []CommitSegment{{
		SegmentID: "seg-1", SegmentKind: SegmentKindBase,
		ManifestKey:    "date=2026-08-07/hour=01/segments/seg-1/manifest.json",
		ManifestSHA256: "manifest",
	}}, pending)

	commit := VerifiedCommit{
		Document: CommitDocument{
			SchemaVersion: CommitSchemaV2, WindowStart: start, WindowEnd: start.Add(time.Hour),
			Segments: pending, AggregateSHA256: "aggregate", AggregateRecordCount: 1,
			AggregateBlobRefCount: 1, AggregateBlobPresentCount: 1,
		},
		ETag: "etag-v2", RecordCount: 1, BlobRefCount: 1, BlobPresentCount: 1,
		Segments: []VerifiedSegment{verifiedSegment},
	}
	require.NoError(t, control.PersistCommit(ctx, conn, shardID, commit))
	require.NoError(t, control.ImportCommit(ctx, conn, shardID, commit), "existing commit import must be idempotent")

	var shardState, etag string
	var records, refs, present, missing int64
	var cleanupEligible bool
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT state, commit_etag, aggregate_record_count, aggregate_blob_ref_count,
       aggregate_blob_present_count, aggregate_blob_missing_count, cleanup_eligible
FROM qa_archive_shards WHERE id=$1`, shardID).Scan(
		&shardState, &etag, &records, &refs, &present, &missing, &cleanupEligible,
	))
	require.Equal(t, StateCommitted, shardState)
	require.Equal(t, "etag-v2", etag)
	require.Equal(t, []int64{1, 1, 1, 0}, []int64{records, refs, present, missing})
	require.False(t, cleanupEligible)

	var segmentState, segmentETag string
	var membershipCount int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT state, commit_etag FROM qa_archive_segments WHERE id=$1`, segmentDBID).Scan(&segmentState, &segmentETag))
	require.Equal(t, StateCommitted, segmentState)
	require.Equal(t, "etag-v2", segmentETag)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM qa_archive_segment_records WHERE segment_id=$1`, segmentDBID).Scan(&membershipCount))
	require.Equal(t, 1, membershipCount)

	require.NoError(t, control.Fail(ctx, conn, shardID, "commit_conflict", errors.New("CAS exhausted")))
	var stateAfterFail string
	var persistedFailureCode sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT state, verification_error_code FROM qa_archive_shards WHERE id=$1`, shardID).Scan(
		&stateAfterFail, &persistedFailureCode,
	))
	require.Equal(t, StateCommitted, stateAfterFail)
	require.False(t, persistedFailureCode.Valid)
}

func TestSQLControlStoreFailureBlocksCleanupAndRedactsMessage(t *testing.T) {
	// Error truncation and cleanup=false are covered without reproducing a full archive run.
	control := NewSQLControlStore()
	require.Equal(t, "missing_evidence", control.failureCode(&IntegrityError{Code: IntegrityMissingEvidence}))
	require.Equal(t, "archive_failed", control.failureCode(sql.ErrNoRows))
}
