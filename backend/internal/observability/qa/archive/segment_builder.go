package archive

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	"github.com/parquet-go/parquet-go"
)

const (
	defaultSegmentPageSize    = 500
	maxRowsPerParquetRowGroup = 250
	parquetPageBufferSize     = 32 * 1024
	defaultScratchFreeBytes   = 2 << 30

	IntegrityMissingEvidence = "missing_evidence"
)

type IntegrityError struct {
	Code      string
	RequestID string
	BlobField string
	BlobURI   string
	Err       error
}

func (e *IntegrityError) Error() string {
	return fmt.Sprintf("qa archive integrity %s request=%s field=%s: %v", e.Code, e.RequestID, e.BlobField, e.Err)
}

func (e *IntegrityError) Unwrap() error { return e.Err }

type BuildInput struct {
	WindowStart         time.Time
	WindowEnd           time.Time
	SegmentKind         string
	BlobRoot            string
	ScratchRoot         string
	PageSize            int
	MinScratchFreeBytes int64
	ScratchFreeBytes    func(string) (int64, error)
}

type BuiltArtifact struct {
	Name        string
	Path        string
	Size        int64
	SHA256      string
	ContentType string
}

type RecordIdentity struct {
	CreatedAt time.Time `json:"created_at"`
	RequestID string    `json:"request_id"`
}

type BuiltSegment struct {
	SegmentID    string
	ScratchDir   string
	RecordsPath  string
	IdentityPath string
	Manifest     SegmentManifest
	Artifacts    []BuiltArtifact
}

func (b *BuiltSegment) Close() error {
	if b == nil || b.ScratchDir == "" {
		return nil
	}
	return os.RemoveAll(b.ScratchDir)
}

type hashingWriter struct {
	writer io.Writer
	hash   hash.Hash
	size   int64
}

func newHashingWriter(writer io.Writer) *hashingWriter {
	return &hashingWriter{writer: writer, hash: sha256.New()}
}

func (w *hashingWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n > 0 {
		_, _ = w.hash.Write(p[:n])
		w.size += int64(n)
	}
	return n, err
}

func (w *hashingWriter) sum() string { return hex.EncodeToString(w.hash.Sum(nil)) }

func BuildSegment(ctx context.Context, conn *sql.Conn, input BuildInput) (_ BuiltSegment, resultErr error) {
	input.WindowStart = input.WindowStart.UTC()
	input.WindowEnd = input.WindowEnd.UTC()
	if !input.WindowEnd.After(input.WindowStart) {
		return BuiltSegment{}, fmt.Errorf("invalid archive window")
	}
	if input.SegmentKind != SegmentKindBase && input.SegmentKind != SegmentKindDelta {
		return BuiltSegment{}, fmt.Errorf("invalid segment kind %q", input.SegmentKind)
	}
	if input.PageSize <= 0 {
		input.PageSize = defaultSegmentPageSize
	}
	if strings.TrimSpace(input.ScratchRoot) == "" {
		return BuiltSegment{}, fmt.Errorf("scratch root is required")
	}

	if err := os.MkdirAll(input.ScratchRoot, 0o700); err != nil {
		return BuiltSegment{}, fmt.Errorf("create scratch root: %w", err)
	}
	if err := os.Chmod(input.ScratchRoot, 0o700); err != nil {
		return BuiltSegment{}, fmt.Errorf("secure scratch root: %w", err)
	}
	if input.MinScratchFreeBytes <= 0 {
		input.MinScratchFreeBytes = defaultScratchFreeBytes
	}
	if input.ScratchFreeBytes == nil {
		input.ScratchFreeBytes = scratchFreeBytes
	}
	available, err := input.ScratchFreeBytes(input.ScratchRoot)
	if err != nil {
		return BuiltSegment{}, fmt.Errorf("inspect scratch space: %w", err)
	}
	if available < input.MinScratchFreeBytes {
		return BuiltSegment{}, fmt.Errorf("insufficient scratch space: available=%d required=%d", available, input.MinScratchFreeBytes)
	}
	segmentID := uuid.NewString()
	scratchDir, err := os.MkdirTemp(input.ScratchRoot, "qa-archive-"+segmentID+"-")
	if err != nil {
		return BuiltSegment{}, fmt.Errorf("create scratch directory: %w", err)
	}
	if err := os.Chmod(scratchDir, 0o700); err != nil {
		_ = os.RemoveAll(scratchDir)
		return BuiltSegment{}, fmt.Errorf("secure scratch directory: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(scratchDir)
		}
	}()

	recordsFile, recordsHash, err := createHashedFile(scratchDir, "records.parquet")
	if err != nil {
		return BuiltSegment{}, err
	}
	defer func() { _ = recordsFile.Close() }()
	evidenceFile, evidenceHash, err := createHashedFile(scratchDir, "evidence.pack")
	if err != nil {
		return BuiltSegment{}, err
	}
	defer func() { _ = evidenceFile.Close() }()
	indexFile, indexHash, err := createHashedFile(scratchDir, "evidence-index.jsonl.zst")
	if err != nil {
		return BuiltSegment{}, err
	}
	defer func() { _ = indexFile.Close() }()
	identityFile, _, err := createHashedFile(scratchDir, "record-identities.jsonl")
	if err != nil {
		return BuiltSegment{}, err
	}
	defer func() { _ = identityFile.Close() }()

	parquetWriter := parquet.NewGenericWriter[RecordRow](
		recordsHash,
		parquet.MaxRowsPerRowGroup(maxRowsPerParquetRowGroup),
		parquet.PageBufferSize(parquetPageBufferSize),
	)
	indexZstd, err := zstd.NewWriter(indexHash)
	if err != nil {
		return BuiltSegment{}, fmt.Errorf("open evidence index encoder: %w", err)
	}
	identityWriter := bufio.NewWriter(identityFile)
	indexWriter := bufio.NewWriter(indexZstd)

	var stats rowStats
	var present, missing int64
	cursorTime := input.WindowStart
	cursorRequestID := ""
	for {
		rows, lastTime, lastRequestID, err := loadRecordPage(ctx, conn, input, cursorTime, cursorRequestID)
		if err != nil {
			return BuiltSegment{}, err
		}
		if len(rows) == 0 {
			break
		}
		if _, err := parquetWriter.Write(rows); err != nil {
			return BuiltSegment{}, fmt.Errorf("write records parquet: %w", err)
		}
		for _, row := range rows {
			stats.recordCount++
			stats.logicalBytes += int64(len(row.RequestID)) + row.DurationMS
			identity := RecordIdentity{CreatedAt: time.UnixMicro(row.CreatedAt).UTC(), RequestID: row.RequestID}
			encoded, err := json.Marshal(identity)
			if err != nil {
				return BuiltSegment{}, err
			}
			if _, err := identityWriter.Write(append(encoded, '\n')); err != nil {
				return BuiltSegment{}, err
			}
			for _, ref := range orderedBlobRefs(row) {
				if ref.URI == nil || strings.TrimSpace(*ref.URI) == "" {
					continue
				}
				stats.blobRefCount++
				line, err := appendEvidenceFile(evidenceHash, input.BlobRoot, row.RequestID, ref.Field, *ref.URI)
				if err != nil {
					missing++
					return BuiltSegment{}, &IntegrityError{
						Code: IntegrityMissingEvidence, RequestID: row.RequestID,
						BlobField: ref.Field, BlobURI: *ref.URI, Err: err,
					}
				}
				present++
				indexBytes, err := json.Marshal(line)
				if err != nil {
					return BuiltSegment{}, err
				}
				if _, err := indexWriter.Write(append(indexBytes, '\n')); err != nil {
					return BuiltSegment{}, err
				}
			}
		}
		cursorTime, cursorRequestID = lastTime, lastRequestID
	}

	if err := parquetWriter.Close(); err != nil {
		return BuiltSegment{}, fmt.Errorf("close records parquet: %w", err)
	}
	if err := identityWriter.Flush(); err != nil {
		return BuiltSegment{}, fmt.Errorf("flush identities: %w", err)
	}
	if err := indexWriter.Flush(); err != nil {
		return BuiltSegment{}, fmt.Errorf("flush evidence index: %w", err)
	}
	if err := indexZstd.Close(); err != nil {
		return BuiltSegment{}, fmt.Errorf("close evidence index: %w", err)
	}
	for _, file := range []*os.File{recordsFile, evidenceFile, indexFile, identityFile} {
		if err := file.Sync(); err != nil {
			return BuiltSegment{}, fmt.Errorf("sync scratch artifact: %w", err)
		}
		if err := file.Close(); err != nil {
			return BuiltSegment{}, fmt.Errorf("close scratch artifact: %w", err)
		}
	}

	artifacts := []BuiltArtifact{
		{Name: "records.parquet", Path: recordsFile.Name(), Size: recordsHash.size, SHA256: recordsHash.sum(), ContentType: "application/vnd.apache.parquet"},
	}
	if evidenceHash.size > 0 {
		artifacts = append(artifacts, BuiltArtifact{Name: "evidence.pack", Path: evidenceFile.Name(), Size: evidenceHash.size, SHA256: evidenceHash.sum(), ContentType: "application/octet-stream"})
	}
	if indexHash.size > 0 {
		artifacts = append(artifacts, BuiltArtifact{Name: "evidence-index.jsonl.zst", Path: indexFile.Name(), Size: indexHash.size, SHA256: indexHash.sum(), ContentType: "application/octet-stream"})
	}

	manifest := SegmentManifest{
		SchemaVersion: ManifestSchemaV1, SegmentID: segmentID, SegmentKind: input.SegmentKind,
		WindowStart: input.WindowStart, WindowEnd: input.WindowEnd,
		RecordCount: stats.recordCount, BlobRefCount: stats.blobRefCount,
		BlobPresentCount: present, BlobMissingCount: missing,
		LogicalBytes:  stats.logicalBytes,
		ArtifactBytes: recordsHash.size + evidenceHash.size + indexHash.size,
		RecordsSHA256: recordsHash.sum(),
	}
	if evidenceHash.size > 0 {
		manifest.EvidencePackSHA256 = evidenceHash.sum()
	}
	if indexHash.size > 0 {
		manifest.EvidenceIndexSHA256 = indexHash.sum()
	}
	manifestBytes, err := MarshalJSON(manifest)
	if err != nil {
		return BuiltSegment{}, err
	}
	manifestPath := filepath.Join(scratchDir, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		return BuiltSegment{}, fmt.Errorf("write manifest: %w", err)
	}
	artifacts = append(artifacts, BuiltArtifact{
		Name: "manifest.json", Path: manifestPath, Size: int64(len(manifestBytes)),
		SHA256: SHA256Hex(manifestBytes), ContentType: "application/json",
	})

	return BuiltSegment{
		SegmentID: segmentID, ScratchDir: scratchDir,
		RecordsPath: recordsFile.Name(), IdentityPath: identityFile.Name(),
		Manifest: manifest, Artifacts: artifacts,
	}, nil
}

func scratchFreeBytes(path string) (int64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return int64(stats.Bavail) * int64(stats.Bsize), nil
}

func createHashedFile(root, name string) (*os.File, *hashingWriter, error) {
	path := filepath.Join(root, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s: %w", name, err)
	}
	return file, newHashingWriter(file), nil
}

type blobRef struct {
	Field string
	URI   *string
}

func orderedBlobRefs(row RecordRow) []blobRef {
	return []blobRef{
		{Field: "blob_uri", URI: row.BlobURI},
		{Field: "request_blob_uri", URI: row.RequestBlobURI},
		{Field: "response_blob_uri", URI: row.ResponseBlobURI},
		{Field: "stream_blob_uri", URI: row.StreamBlobURI},
	}
}

func appendEvidenceFile(pack *hashingWriter, blobRoot, requestID, field, blobURI string) (evidenceIndexLine, error) {
	path, err := localEvidencePath(blobRoot, blobURI)
	if err != nil {
		return evidenceIndexLine{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return evidenceIndexLine{}, err
	}
	defer func() { _ = file.Close() }()
	start := pack.size
	digest := sha256.New()
	length, err := io.Copy(io.MultiWriter(pack, digest), file)
	if err != nil {
		return evidenceIndexLine{}, err
	}
	return evidenceIndexLine{
		RequestID: requestID, BlobField: field, BlobURI: blobURI,
		Offset: start, Length: length, SHA256: hex.EncodeToString(digest.Sum(nil)),
	}, nil
}

func loadRecordPage(
	ctx context.Context,
	conn *sql.Conn,
	input BuildInput,
	cursorTime time.Time,
	cursorRequestID string,
) ([]RecordRow, time.Time, string, error) {
	exclusion := ""
	if input.SegmentKind == SegmentKindDelta {
		exclusion = `
		   AND NOT EXISTS (
		       SELECT 1
		         FROM qa_archive_segment_records sr
		         JOIN qa_archive_segments s ON s.id = sr.segment_id
		        WHERE sr.created_at = q.created_at
		          AND sr.request_id = q.request_id
		          AND s.state IN ('verified', 'committed')
		   )`
	}
	query := `
		SELECT q.request_id, q.trajectory_id, q.user_id, q.group_id, q.api_key_id, q.account_id,
		       q.platform, q.provider, q.requested_model, q.upstream_model, q.status_code, q.success,
		       q.duration_ms, q.stream, q.input_tokens, q.output_tokens,
		       q.request_sha256, q.response_sha256,
		       q.blob_uri, q.request_blob_uri, q.response_blob_uri, q.stream_blob_uri,
		       q.capture_status, q.created_at
		  FROM qa_records q
		 WHERE q.created_at >= $1 AND q.created_at < $2
		   AND (q.created_at, q.request_id) > ($3, $4)` + exclusion + `
		 ORDER BY q.created_at, q.request_id
		 LIMIT $5`
	rows, err := conn.QueryContext(ctx, query, input.WindowStart, input.WindowEnd, cursorTime, cursorRequestID, input.PageSize)
	if err != nil {
		return nil, time.Time{}, "", fmt.Errorf("query qa_records page: %w", err)
	}
	defer func() { _ = rows.Close() }()

	page := make([]RecordRow, 0, input.PageSize)
	var lastTime time.Time
	var lastRequestID string
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
			&blobURI, &reqBlob, &respBlob, &streamBlob, &row.CaptureStatus, &createdAt,
		); err != nil {
			return nil, time.Time{}, "", fmt.Errorf("scan qa_record: %w", err)
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
		page = append(page, row)
		lastTime, lastRequestID = createdAt.UTC(), row.RequestID
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, "", err
	}
	return page, lastTime, lastRequestID, nil
}
