package archive

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lib/pq"
)

const identityInsertBatchSize = 500

type SQLControlStore struct{}

func NewSQLControlStore() *SQLControlStore { return &SQLControlStore{} }

func (s *SQLControlStore) EnsureShard(ctx context.Context, conn *sql.Conn, window Window) (int64, error) {
	var id int64
	err := conn.QueryRowContext(ctx, `
INSERT INTO qa_archive_shards (
    window_start, window_end, generation, state, s3_prefix, first_attempt_at, updated_at,
    cleanup_eligible
) VALUES ($1, $2, 0, $3, $4, now(), now(), false)
ON CONFLICT (window_start, generation) DO UPDATE SET
    window_end = EXCLUDED.window_end,
    s3_prefix = EXCLUDED.s3_prefix,
    updated_at = now(),
    cleanup_eligible = false
RETURNING id`,
		window.Start, window.End, StatePending, ShardPrefix(window.Start),
	).Scan(&id)
	return id, err
}

func (s *SQLControlStore) ImportCommit(ctx context.Context, conn *sql.Conn, shardID int64, commit VerifiedCommit) error {
	if len(commit.Document.Segments) != len(commit.Segments) {
		return fmt.Errorf("commit descriptor/verification count mismatch")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for index, descriptor := range commit.Document.Segments {
		verified := commit.Segments[index]
		segmentID, err := upsertVerifiedSegment(ctx, tx, shardID, descriptor, verified, StateCommitted, commit.ETag)
		if err != nil {
			return err
		}
		if err := replaceSegmentIdentities(ctx, tx, segmentID, verified.IdentityPath); err != nil {
			return err
		}
	}
	if err := persistCommitTx(ctx, tx, shardID, commit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLControlStore) OrphanIncomplete(ctx context.Context, conn *sql.Conn, shardID int64) error {
	_, err := conn.ExecContext(ctx, `
UPDATE qa_archive_segments SET
    state='orphaned', verification_error_code='interrupted_before_verify',
    last_error='previous archive attempt ended before verification', updated_at=now()
WHERE shard_id=$1 AND state='writing'`, shardID)
	return err
}

func (s *SQLControlStore) PendingVerified(ctx context.Context, conn *sql.Conn, shardID int64) ([]CommitSegment, error) {
	rows, err := conn.QueryContext(ctx, `
SELECT segment_id, segment_kind, manifest_key, checksums->>'manifest_sha256'
FROM qa_archive_segments
WHERE shard_id=$1 AND state=$2
ORDER BY id`, shardID, StateVerified)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CommitSegment
	for rows.Next() {
		var item CommitSegment
		if err := rows.Scan(&item.SegmentID, &item.SegmentKind, &item.ManifestKey, &item.ManifestSHA256); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLControlStore) StartSegment(ctx context.Context, conn *sql.Conn, shardID int64, built BuiltSegment, prefix string) (int64, error) {
	manifestArtifact, ok := builtArtifact(built, "manifest.json")
	if !ok || manifestArtifact.SHA256 == "" {
		return 0, fmt.Errorf("built segment has no manifest checksum")
	}
	checksums, err := segmentChecksumsJSON(built.Manifest, manifestArtifact.SHA256)
	if err != nil {
		return 0, err
	}
	var id int64
	err = conn.QueryRowContext(ctx, `
INSERT INTO qa_archive_segments (
    shard_id, segment_id, segment_kind, state, attempt_id,
    manifest_key, records_key, evidence_pack_key, evidence_index_key,
    record_count, blob_ref_count, blob_present_count, blob_missing_count,
    logical_bytes, artifact_bytes, checksums, created_at, updated_at
) VALUES (
    $1,$2,$3,$4,$5,$6,$7,$8,$9,
    $10,$11,$12,$13,$14,$15,$16::jsonb,now(),now()
)
ON CONFLICT (shard_id, segment_id) DO UPDATE SET
    state=EXCLUDED.state, attempt_id=EXCLUDED.attempt_id,
    manifest_key=EXCLUDED.manifest_key, records_key=EXCLUDED.records_key,
    evidence_pack_key=EXCLUDED.evidence_pack_key,
    evidence_index_key=EXCLUDED.evidence_index_key,
    record_count=EXCLUDED.record_count, blob_ref_count=EXCLUDED.blob_ref_count,
    blob_present_count=EXCLUDED.blob_present_count,
    blob_missing_count=EXCLUDED.blob_missing_count,
    logical_bytes=EXCLUDED.logical_bytes, artifact_bytes=EXCLUDED.artifact_bytes,
    checksums=EXCLUDED.checksums, updated_at=now(),
    verification_error_code=NULL, last_error=NULL
WHERE qa_archive_segments.state IN ('writing','failed','orphaned')
RETURNING id`,
		shardID, built.SegmentID, built.Manifest.SegmentKind, StateWriting, built.SegmentID,
		prefix+"/manifest.json", prefix+"/records.parquet", nullableArtifactKey(built, prefix, "evidence.pack"), nullableArtifactKey(built, prefix, "evidence-index.jsonl.zst"),
		built.Manifest.RecordCount, built.Manifest.BlobRefCount, built.Manifest.BlobPresentCount,
		built.Manifest.BlobMissingCount, built.Manifest.LogicalBytes, built.Manifest.ArtifactBytes, string(checksums),
	).Scan(&id)
	return id, err
}

func (s *SQLControlStore) MarkSegmentVerified(ctx context.Context, conn *sql.Conn, segmentDBID int64, verified VerifiedSegment) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
UPDATE qa_archive_segments SET
    state=$1, verified_at=now(), updated_at=now(),
    record_count=$2, blob_ref_count=$3, blob_present_count=$4, blob_missing_count=$5,
    logical_bytes=$6, artifact_bytes=$7,
    verification_error_code=NULL, last_error=NULL
WHERE id=$8 AND state IN ('writing','failed','orphaned')`,
		StateVerified, verified.Manifest.RecordCount, verified.Manifest.BlobRefCount,
		verified.Manifest.BlobPresentCount, verified.Manifest.BlobMissingCount,
		verified.Manifest.LogicalBytes, verified.Manifest.ArtifactBytes, segmentDBID,
	)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("segment %d was not mutable", segmentDBID)
	}
	if err := replaceSegmentIdentities(ctx, tx, segmentDBID, verified.IdentityPath); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLControlStore) FailSegment(ctx context.Context, conn *sql.Conn, segmentID int64, code string, failure error) error {
	message := "archive segment failed"
	if failure != nil {
		message = failure.Error()
	}
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := conn.ExecContext(ctx, `
UPDATE qa_archive_segments SET
    state='failed', verification_error_code=$1, last_error=$2, updated_at=now()
WHERE id=$3 AND state='writing'`, code, message, segmentID)
	return err
}

func (s *SQLControlStore) PersistCommit(ctx context.Context, conn *sql.Conn, shardID int64, commit VerifiedCommit) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := persistCommitTx(ctx, tx, shardID, commit); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLControlStore) Fail(ctx context.Context, conn *sql.Conn, shardID int64, code string, failure error) error {
	if conn == nil {
		return nil
	}
	if strings.TrimSpace(code) == "" {
		code = s.failureCode(failure)
	}
	message := "archive failed"
	if failure != nil {
		message = failure.Error()
	}
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := conn.ExecContext(ctx, `
UPDATE qa_archive_shards SET
    state=$1, verification_error_code=$2, last_error=$3,
    cleanup_eligible=false, updated_at=now()
WHERE id=$4`, StateFailed, code, message, shardID)
	return err
}

func (s *SQLControlStore) failureCode(err error) string {
	var integrity *IntegrityError
	if errors.As(err, &integrity) && integrity.Code != "" {
		return integrity.Code
	}
	return "archive_failed"
}

func upsertVerifiedSegment(
	ctx context.Context,
	tx *sql.Tx,
	shardID int64,
	descriptor CommitSegment,
	verified VerifiedSegment,
	state, etag string,
) (int64, error) {
	checksums, err := segmentChecksumsJSON(verified.Manifest, descriptor.ManifestSHA256)
	if err != nil {
		return 0, err
	}
	prefix := strings.TrimSuffix(descriptor.ManifestKey, "/manifest.json")
	var id int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO qa_archive_segments (
    shard_id, segment_id, segment_kind, state, attempt_id,
    manifest_key, records_key, evidence_pack_key, evidence_index_key,
    record_count, blob_ref_count, blob_present_count, blob_missing_count,
    logical_bytes, artifact_bytes, checksums, verified_at, committed_at,
    commit_etag, created_at, updated_at
) VALUES (
    $1,$2,$3,$4,$2,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,
    now(),CASE WHEN $4='committed' THEN now() ELSE NULL END,$16,now(),now()
)
ON CONFLICT (shard_id, segment_id) DO UPDATE SET
    state=EXCLUDED.state, manifest_key=EXCLUDED.manifest_key,
    records_key=EXCLUDED.records_key, evidence_pack_key=EXCLUDED.evidence_pack_key,
    evidence_index_key=EXCLUDED.evidence_index_key,
    record_count=EXCLUDED.record_count, blob_ref_count=EXCLUDED.blob_ref_count,
    blob_present_count=EXCLUDED.blob_present_count,
    blob_missing_count=EXCLUDED.blob_missing_count,
    logical_bytes=EXCLUDED.logical_bytes, artifact_bytes=EXCLUDED.artifact_bytes,
    checksums=EXCLUDED.checksums, verified_at=COALESCE(qa_archive_segments.verified_at, now()),
    committed_at=CASE WHEN EXCLUDED.state='committed' THEN now() ELSE qa_archive_segments.committed_at END,
    commit_etag=EXCLUDED.commit_etag, verification_error_code=NULL, last_error=NULL, updated_at=now()
RETURNING id`,
		shardID, descriptor.SegmentID, descriptor.SegmentKind, state, descriptor.ManifestKey,
		prefix+"/records.parquet", nullableKey(verified.Manifest.BlobRefCount > 0, prefix+"/evidence.pack"),
		nullableKey(verified.Manifest.BlobRefCount > 0, prefix+"/evidence-index.jsonl.zst"),
		verified.Manifest.RecordCount, verified.Manifest.BlobRefCount,
		verified.Manifest.BlobPresentCount, verified.Manifest.BlobMissingCount,
		verified.Manifest.LogicalBytes, verified.Manifest.ArtifactBytes, string(checksums), etag,
	).Scan(&id)
	return id, err
}

func persistCommitTx(ctx context.Context, tx *sql.Tx, shardID int64, commit VerifiedCommit) error {
	segmentIDs := make([]string, 0, len(commit.Document.Segments))
	for _, segment := range commit.Document.Segments {
		segmentIDs = append(segmentIDs, segment.SegmentID)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE qa_archive_segments SET
    state=$1, committed_at=COALESCE(committed_at, now()), commit_etag=$2,
    updated_at=now(), verification_error_code=NULL, last_error=NULL
WHERE shard_id=$3 AND segment_id=ANY($4) AND state IN ('verified','committed')`,
		StateCommitted, commit.ETag, shardID, pq.Array(segmentIDs))
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != int64(len(segmentIDs)) {
		return fmt.Errorf("committed segment control mismatch: changed=%d expected=%d", changed, len(segmentIDs))
	}
	checksums, err := json.Marshal(map[string]string{
		"aggregate_sha256": commit.Document.AggregateSHA256,
		"commit_etag":      commit.ETag,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE qa_archive_shards SET
    state=$1, commit_etag=$2, commit_key=$3,
    aggregate_record_count=$4, aggregate_blob_ref_count=$5,
    aggregate_blob_present_count=$6, aggregate_blob_missing_count=$7,
    record_count=$4, blob_ref_count=$5, blob_present_count=$6, blob_missing_count=$7,
    checksums=$8::jsonb, verified_at=COALESCE(verified_at, now()),
    restore_verified_at=now(), completed_at=now(), last_reconciled_at=now(),
    cleanup_eligible=false, verification_error_code=NULL, last_error=NULL, updated_at=now()
WHERE id=$9`,
		StateCommitted, commit.ETag, ShardRelativePrefix(commit.Document.WindowStart)+"/commit.json",
		commit.RecordCount, commit.BlobRefCount, commit.BlobPresentCount, commit.BlobMissingCount,
		string(checksums), shardID,
	)
	return err
}

func replaceSegmentIdentities(ctx context.Context, tx *sql.Tx, segmentID int64, path string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM qa_archive_segment_records WHERE segment_id=$1`, segmentID); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 16*1024), maxManifestBytes)
	batch := make([]RecordIdentity, 0, identityInsertBatchSize)
	for scanner.Scan() {
		var identity RecordIdentity
		if err := json.Unmarshal(scanner.Bytes(), &identity); err != nil {
			return err
		}
		batch = append(batch, identity)
		if len(batch) == cap(batch) {
			if err := insertIdentityBatch(ctx, tx, segmentID, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return insertIdentityBatch(ctx, tx, segmentID, batch)
}

func insertIdentityBatch(ctx context.Context, tx *sql.Tx, segmentID int64, identities []RecordIdentity) error {
	if len(identities) == 0 {
		return nil
	}
	args := make([]any, 0, len(identities)*3)
	values := make([]string, 0, len(identities))
	for index, identity := range identities {
		base := index * 3
		values = append(values, fmt.Sprintf("($%d,$%d,$%d)", base+1, base+2, base+3))
		args = append(args, segmentID, identity.CreatedAt.UTC(), identity.RequestID)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO qa_archive_segment_records (segment_id, created_at, request_id) VALUES `+strings.Join(values, ","), args...)
	return err
}

func segmentChecksumsJSON(manifest SegmentManifest, manifestSHA string) ([]byte, error) {
	return json.Marshal(map[string]string{
		"manifest_sha256":       manifestSHA,
		"records_sha256":        manifest.RecordsSHA256,
		"evidence_pack_sha256":  manifest.EvidencePackSHA256,
		"evidence_index_sha256": manifest.EvidenceIndexSHA256,
	})
}

func nullableArtifactKey(built BuiltSegment, prefix, name string) any {
	_, ok := builtArtifact(built, name)
	return nullableKey(ok, prefix+"/"+name)
}

func nullableKey(present bool, value string) any {
	if !present {
		return nil
	}
	return value
}

var _ ReconcileControl = (*SQLControlStore)(nil)
