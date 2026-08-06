package archive

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
)

// RecordRow is the parquet projection of qa_records metadata (design §8.2).
type RecordRow struct {
	RequestID       string  `parquet:"request_id"`
	TrajectoryID    *string `parquet:"trajectory_id,optional"`
	UserID          int64   `parquet:"user_id"`
	GroupID         *int64  `parquet:"group_id,optional"`
	APIKeyID        int64   `parquet:"api_key_id"`
	AccountID       *int64  `parquet:"account_id,optional"`
	Platform        string  `parquet:"platform"`
	Provider        *string `parquet:"provider,optional"`
	RequestedModel  string  `parquet:"requested_model"`
	UpstreamModel   *string `parquet:"upstream_model,optional"`
	StatusCode      int     `parquet:"status_code"`
	Success         bool    `parquet:"success"`
	DurationMS      int64   `parquet:"duration_ms"`
	Stream          bool    `parquet:"stream"`
	InputTokens     int     `parquet:"input_tokens"`
	OutputTokens    int     `parquet:"output_tokens"`
	RequestSHA256   string  `parquet:"request_sha256"`
	ResponseSHA256  string  `parquet:"response_sha256"`
	BlobURI         *string `parquet:"blob_uri,optional"`
	RequestBlobURI  *string `parquet:"request_blob_uri,optional"`
	ResponseBlobURI *string `parquet:"response_blob_uri,optional"`
	StreamBlobURI   *string `parquet:"stream_blob_uri,optional"`
	CaptureStatus   string  `parquet:"capture_status"`
	CreatedAt       int64   `parquet:"created_at, timestamp(microsecond)"`
}

type evidenceIndexLine struct {
	RequestID string `json:"request_id"`
	BlobField string `json:"blob_field"`
	BlobURI   string `json:"blob_uri"`
	Offset    int64  `json:"offset"`
	Length    int64  `json:"length"`
	SHA256    string `json:"sha256"`
}

// UploadInput configures a base-segment upload for one UTC hour.
type UploadInput struct {
	WindowStart time.Time
	WindowEnd   time.Time
	BlobRoot    string
}

// UploadResult summarizes a committed base segment.
type UploadResult struct {
	SegmentID        string
	ManifestKey      string
	CommitKey        string
	RecordCount      int64
	BlobRefCount     int64
	BlobPresentCount int64
	BlobMissingCount int64
	LogicalBytes     int64
	ArtifactBytes    int64
	Checksums        map[string]string
}

// UploadBaseSegment writes immutable base segment artifacts and commit.json (design §8.2).
func UploadBaseSegment(
	ctx context.Context,
	conn *sql.Conn,
	store ObjectStore,
	in UploadInput,
) (UploadResult, error) {
	windowStart := in.WindowStart.UTC()
	windowEnd := in.WindowEnd.UTC()
	if !windowEnd.After(windowStart) {
		return UploadResult{}, fmt.Errorf("invalid archive window")
	}

	segmentID := uuid.NewString()
	relPrefix := ShardRelativePrefix(windowStart)
	segmentPrefix := relPrefix + "/segments/" + segmentID + "/"

	rows, stats, err := loadRecordRows(ctx, conn, windowStart, windowEnd)
	if err != nil {
		return UploadResult{}, err
	}

	recordsBytes, err := encodeRecordsParquet(rows)
	if err != nil {
		return UploadResult{}, fmt.Errorf("encode records parquet: %w", err)
	}
	evidenceBytes, indexLines, present, missing, err := buildEvidencePack(in.BlobRoot, rows)
	if err != nil {
		return UploadResult{}, fmt.Errorf("build evidence pack: %w", err)
	}
	indexBytes, err := encodeEvidenceIndex(indexLines)
	if err != nil {
		return UploadResult{}, fmt.Errorf("encode evidence index: %w", err)
	}

	manifest := SegmentManifest{
		SchemaVersion:       ManifestSchemaV1,
		SegmentID:           segmentID,
		SegmentKind:         SegmentKindBase,
		WindowStart:         windowStart,
		WindowEnd:           windowEnd,
		RecordCount:         stats.recordCount,
		BlobRefCount:        stats.blobRefCount,
		BlobPresentCount:    present,
		BlobMissingCount:    missing,
		LogicalBytes:        stats.logicalBytes,
		ArtifactBytes:       int64(len(recordsBytes) + len(evidenceBytes) + len(indexBytes)),
		RecordsSHA256:       SHA256Hex(recordsBytes),
		EvidencePackSHA256:  SHA256Hex(evidenceBytes),
		EvidenceIndexSHA256: SHA256Hex(indexBytes),
	}
	manifestBytes, err := MarshalJSON(manifest)
	if err != nil {
		return UploadResult{}, err
	}

	artifacts := []struct {
		name        string
		body        []byte
		contentType string
	}{
		{"records.parquet", recordsBytes, "application/vnd.apache.parquet"},
		{"manifest.json", manifestBytes, "application/json"},
	}
	if len(evidenceBytes) > 0 {
		artifacts = append(artifacts, struct {
			name        string
			body        []byte
			contentType string
		}{"evidence.pack", evidenceBytes, "application/octet-stream"})
	}
	if len(indexBytes) > 0 {
		artifacts = append(artifacts, struct {
			name        string
			body        []byte
			contentType string
		}{"evidence-index.jsonl.zst", indexBytes, "application/octet-stream"})
	}

	for _, artifact := range artifacts {
		if err := store.Put(ctx, segmentPrefix+artifact.name, artifact.body, artifact.contentType); err != nil {
			return UploadResult{}, fmt.Errorf("put segment artifact %s: %w", artifact.name, err)
		}
	}

	commitKey := relPrefix + "/commit.json"
	commitDoc := CommitDocument{
		SchemaVersion: CommitSchemaV1,
		WindowStart:   windowStart,
		WindowEnd:     windowEnd,
		Generation:    0,
		Segments: []CommitSegment{{
			SegmentID:      segmentID,
			SegmentKind:    SegmentKindBase,
			ManifestKey:    segmentPrefix + "manifest.json",
			ManifestSHA256: SHA256Hex(manifestBytes),
		}},
		AggregateSHA256: SHA256Hex(manifestBytes),
		CommittedAt:     time.Now().UTC(),
	}
	commitBytes, err := MarshalJSON(commitDoc)
	if err != nil {
		return UploadResult{}, err
	}
	if err := store.PutIfAbsent(ctx, commitKey, commitBytes, "application/json"); err != nil {
		return UploadResult{}, fmt.Errorf("put commit.json: %w", err)
	}

	checksums := map[string]string{
		"records_sha256":        manifest.RecordsSHA256,
		"manifest_sha256":       SHA256Hex(manifestBytes),
		"commit_sha256":         SHA256Hex(commitBytes),
		"evidence_pack_sha256":  manifest.EvidencePackSHA256,
		"evidence_index_sha256": manifest.EvidenceIndexSHA256,
	}
	checksumsJSON, err := MarshalJSON(checksums)
	if err != nil {
		return UploadResult{}, err
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE qa_archive_shards SET
		    state = $1,
		    record_count = $2,
		    blob_ref_count = $3,
		    blob_present_count = $4,
		    blob_missing_count = $5,
		    logical_bytes = $6,
		    artifact_bytes = $7,
		    checksums = $8::jsonb,
		    manifest_key = $9,
		    commit_key = $10,
		    completed_at = $11,
		    updated_at = $11,
		    last_error = NULL
		  WHERE window_start = $12 AND generation = 0
		    AND state IN ('pending', 'writing', 'failed')`,
		StateCommitted,
		stats.recordCount,
		stats.blobRefCount,
		present,
		missing,
		stats.logicalBytes,
		manifest.ArtifactBytes,
		string(checksumsJSON),
		segmentPrefix+"manifest.json",
		commitKey,
		commitDoc.CommittedAt,
		windowStart,
	); err != nil {
		return UploadResult{}, fmt.Errorf("mark shard committed: %w", err)
	}

	return UploadResult{
		SegmentID:        segmentID,
		ManifestKey:      segmentPrefix + "manifest.json",
		CommitKey:        commitKey,
		RecordCount:      stats.recordCount,
		BlobRefCount:     stats.blobRefCount,
		BlobPresentCount: present,
		BlobMissingCount: missing,
		LogicalBytes:     stats.logicalBytes,
		ArtifactBytes:    manifest.ArtifactBytes,
		Checksums:        checksums,
	}, nil
}

type rowStats struct {
	recordCount  int64
	blobRefCount int64
	logicalBytes int64
}

func loadRecordRows(ctx context.Context, conn *sql.Conn, windowStart, windowEnd time.Time) ([]RecordRow, rowStats, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT request_id, trajectory_id, user_id, group_id, api_key_id, account_id,
		       platform, provider, requested_model, upstream_model, status_code, success,
		       duration_ms, stream, input_tokens, output_tokens,
		       request_sha256, response_sha256,
		       blob_uri, request_blob_uri, response_blob_uri, stream_blob_uri,
		       capture_status, created_at
		  FROM qa_records
		 WHERE created_at >= $1 AND created_at < $2
		 ORDER BY created_at, request_id`,
		windowStart, windowEnd,
	)
	if err != nil {
		return nil, rowStats{}, fmt.Errorf("query qa_records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RecordRow
	var stats rowStats
	for rows.Next() {
		var row RecordRow
		var trajectory, provider, upstreamModel sql.NullString
		var groupID, accountID sql.NullInt64
		var blobURI, reqBlob, respBlob, streamBlob sql.NullString
		var createdAt time.Time
		if err := rows.Scan(
			&row.RequestID, &trajectory, &row.UserID, &groupID, &row.APIKeyID, &accountID,
			&row.Platform, &provider, &row.RequestedModel, &upstreamModel, &row.StatusCode, &row.Success,
			&row.DurationMS, &row.Stream, &row.InputTokens, &row.OutputTokens,
			&row.RequestSHA256, &row.ResponseSHA256,
			&blobURI, &reqBlob, &respBlob, &streamBlob,
			&row.CaptureStatus, &createdAt,
		); err != nil {
			return nil, rowStats{}, fmt.Errorf("scan qa_record: %w", err)
		}
		row.TrajectoryID = nullStringPtr(trajectory)
		row.GroupID = nullInt64Ptr(groupID)
		row.AccountID = nullInt64Ptr(accountID)
		row.Provider = nullStringPtr(provider)
		row.UpstreamModel = nullStringPtr(upstreamModel)
		row.BlobURI = nullStringPtr(blobURI)
		row.RequestBlobURI = nullStringPtr(reqBlob)
		row.ResponseBlobURI = nullStringPtr(respBlob)
		row.StreamBlobURI = nullStringPtr(streamBlob)
		row.CreatedAt = createdAt.UTC().UnixMicro()
		out = append(out, row)
		stats.recordCount++
		stats.logicalBytes += int64(len(row.RequestID)) + row.DurationMS
		for _, ref := range []sql.NullString{blobURI, reqBlob, respBlob, streamBlob} {
			if ref.Valid && strings.TrimSpace(ref.String) != "" {
				stats.blobRefCount++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, rowStats{}, err
	}
	return out, stats, nil
}

func encodeRecordsParquet(rows []RecordRow) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	writer := parquet.NewGenericWriter[RecordRow](buf)
	if _, err := writer.Write(rows); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func buildEvidencePack(blobRoot string, rows []RecordRow) ([]byte, []evidenceIndexLine, int64, int64, error) {
	var pack bytes.Buffer
	var index []evidenceIndexLine
	var present, missing int64
	appendRef := func(requestID, field, blobURI string) error {
		blobURI = strings.TrimSpace(blobURI)
		if blobURI == "" {
			return nil
		}
		body, err := readLocalEvidence(blobRoot, blobURI)
		if err != nil {
			missing++
			return nil
		}
		offset := int64(pack.Len())
		if _, err := pack.Write(body); err != nil {
			return err
		}
		index = append(index, evidenceIndexLine{
			RequestID: requestID,
			BlobField: field,
			BlobURI:   blobURI,
			Offset:    offset,
			Length:    int64(len(body)),
			SHA256:    SHA256Hex(body),
		})
		present++
		return nil
	}
	for _, row := range rows {
		for field, uri := range map[string]*string{
			"blob_uri":          row.BlobURI,
			"request_blob_uri":  row.RequestBlobURI,
			"response_blob_uri": row.ResponseBlobURI,
			"stream_blob_uri":   row.StreamBlobURI,
		} {
			if uri == nil {
				continue
			}
			if err := appendRef(row.RequestID, field, *uri); err != nil {
				return nil, nil, present, missing, err
			}
		}
	}
	return pack.Bytes(), index, present, missing, nil
}

func readLocalEvidence(blobRoot, blobURI string) ([]byte, error) {
	path := localEvidencePath(blobRoot, blobURI)
	if path == "" {
		return nil, fmt.Errorf("unsupported blob uri")
	}
	return os.ReadFile(path)
}

func localEvidencePath(blobRoot, blobURI string) string {
	blobURI = strings.TrimSpace(blobURI)
	switch {
	case strings.HasPrefix(blobURI, "file://"):
		return strings.TrimPrefix(blobURI, "file://")
	case strings.HasPrefix(blobURI, "mem://"):
		key := strings.TrimPrefix(blobURI, "mem://")
		return filepath.Join(blobRoot, filepath.FromSlash(key))
	default:
		if blobURI == "" {
		 return ""
		}
		return filepath.Join(blobRoot, filepath.FromSlash(strings.TrimPrefix(blobURI, "/")))
	}
}

func encodeEvidenceIndex(lines []evidenceIndexLine) ([]byte, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	var raw bytes.Buffer
	for _, line := range lines {
		b, err := MarshalJSON(line)
		if err != nil {
			return nil, err
		}
		raw.Write(b)
		raw.WriteByte('\n')
	}
	var out bytes.Buffer
	zw, err := zstd.NewWriter(&out)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw.Bytes()); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullInt64Ptr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
