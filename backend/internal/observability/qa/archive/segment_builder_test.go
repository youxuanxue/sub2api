//go:build unit

package archive

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/parquet-go/parquet-go"
)

var segmentColumns = []string{
	"request_id", "trajectory_id", "user_id", "group_id", "api_key_id", "account_id",
	"platform", "provider", "requested_model", "upstream_model", "status_code", "success",
	"duration_ms", "stream", "input_tokens", "output_tokens", "request_sha256", "response_sha256",
	"blob_uri", "request_blob_uri", "response_blob_uri", "stream_blob_uri", "capture_status", "created_at",
}

func TestBuildSegmentPaginatesToMode0600Files(t *testing.T) {
	db, mock, conn := segmentTestDB(t)
	defer func() { _ = conn.Close(); _ = db.Close() }()

	windowStart := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(time.Hour)
	blobRoot := t.TempDir()
	requestBlob := filepath.Join(blobRoot, "request.json.zst")
	responseBlob := filepath.Join(blobRoot, "response.json.zst")
	if err := os.WriteFile(requestBlob, []byte("request"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(responseBlob, []byte("response"), 0o600); err != nil {
		t.Fatal(err)
	}

	query := regexp.QuoteMeta("FROM qa_records q")
	mock.ExpectQuery(query).
		WithArgs(windowStart, windowEnd, windowStart, "", 1).
		WillReturnRows(segmentRows().AddRow(
			"req-1", nil, int64(1), nil, int64(2), nil, "anthropic", nil, "claude", nil,
			200, true, int64(10), false, 1, 2, "a", "b",
			nil, "file://"+requestBlob, "file://"+responseBlob, nil, "captured", windowStart.Add(time.Minute),
		))
	mock.ExpectQuery(query).
		WithArgs(windowStart, windowEnd, windowStart.Add(time.Minute), "req-1", 1).
		WillReturnRows(segmentRows().AddRow(
			"req-2", nil, int64(1), nil, int64(2), nil, "anthropic", nil, "claude", nil,
			200, true, int64(20), false, 3, 4, "c", "d",
			nil, nil, nil, nil, "captured", windowStart.Add(2*time.Minute),
		))
	mock.ExpectQuery(query).
		WithArgs(windowStart, windowEnd, windowStart.Add(2*time.Minute), "req-2", 1).
		WillReturnRows(segmentRows())

	built, err := BuildSegment(context.Background(), conn, BuildInput{
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		SegmentKind: SegmentKindBase,
		BlobRoot:    blobRoot,
		ScratchRoot: t.TempDir(),
		PageSize:    1,
	})
	if err != nil {
		t.Fatalf("BuildSegment()=%v", err)
	}
	defer func() { _ = built.Close() }()

	if built.Manifest.RecordCount != 2 || built.Manifest.BlobRefCount != 2 ||
		built.Manifest.BlobPresentCount != 2 || built.Manifest.BlobMissingCount != 0 {
		t.Fatalf("manifest=%+v", built.Manifest)
	}
	if built.Manifest.SegmentKind != SegmentKindBase || len(built.Artifacts) != 4 {
		t.Fatalf("built=%+v", built)
	}
	for _, artifact := range built.Artifacts {
		info, statErr := os.Stat(artifact.Path)
		if statErr != nil {
			t.Fatalf("stat %s: %v", artifact.Name, statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode=%o", artifact.Name, info.Mode().Perm())
		}
		if info.Size() != artifact.Size {
			t.Fatalf("%s size=%d want=%d", artifact.Name, info.Size(), artifact.Size)
		}
	}

	recordFile, err := os.Open(built.RecordsPath)
	if err != nil {
		t.Fatal(err)
	}
	reader := parquet.NewGenericReader[RecordRow](recordFile)
	rows := make([]RecordRow, 2)
	n, err := reader.Read(rows)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read parquet: %v", err)
	}
	_ = reader.Close()
	_ = recordFile.Close()
	if n != 2 || rows[0].RequestID != "req-1" || rows[1].RequestID != "req-2" {
		t.Fatalf("parquet rows=%+v n=%d", rows, n)
	}

	identityFile, err := os.Open(built.IdentityPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = identityFile.Close() }()
	scanner := bufio.NewScanner(identityFile)
	var identities []RecordIdentity
	for scanner.Scan() {
		var identity RecordIdentity
		if err := json.Unmarshal(scanner.Bytes(), &identity); err != nil {
			t.Fatal(err)
		}
		identities = append(identities, identity)
	}
	if len(identities) != 2 || identities[0].RequestID != "req-1" || identities[1].RequestID != "req-2" {
		t.Fatalf("identities=%+v", identities)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildDeltaExcludesCommittedMembershipInSQL(t *testing.T) {
	db, mock, conn := segmentTestDB(t)
	defer func() { _ = conn.Close(); _ = db.Close() }()

	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	query := `(?s)FROM qa_records q.*NOT EXISTS.*qa_archive_segment_records.*s.state IN \('verified', 'committed'\)`
	mock.ExpectQuery(query).
		WithArgs(start, start.Add(time.Hour), start, "", 50).
		WillReturnRows(segmentRows())

	built, err := BuildSegment(context.Background(), conn, BuildInput{
		WindowStart: start,
		WindowEnd:   start.Add(time.Hour),
		SegmentKind: SegmentKindDelta,
		BlobRoot:    t.TempDir(),
		ScratchRoot: t.TempDir(),
		PageSize:    50,
	})
	if err != nil {
		t.Fatalf("BuildSegment()=%v", err)
	}
	defer func() { _ = built.Close() }()
	if built.Manifest.RecordCount != 0 || built.Manifest.SegmentKind != SegmentKindDelta {
		t.Fatalf("manifest=%+v", built.Manifest)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSegmentCreatesSecureScratchRoot(t *testing.T) {
	db, mock, conn := segmentTestDB(t)
	defer func() { _ = conn.Close(); _ = db.Close() }()
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("FROM qa_records q")).
		WithArgs(start, start.Add(time.Hour), start, "", 100).
		WillReturnRows(segmentRows())
	scratch := filepath.Join(t.TempDir(), "not-created", "qa_archive_tmp")

	built, err := BuildSegment(context.Background(), conn, BuildInput{
		WindowStart: start, WindowEnd: start.Add(time.Hour), SegmentKind: SegmentKindBase,
		BlobRoot: t.TempDir(), ScratchRoot: scratch, PageSize: 100,
	})
	if err != nil {
		t.Fatalf("BuildSegment()=%v", err)
	}
	defer func() { _ = built.Close() }()
	info, err := os.Stat(scratch)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("scratch mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestBuildSegmentMissingEvidenceFailsAndRemovesScratch(t *testing.T) {
	db, mock, conn := segmentTestDB(t)
	defer func() { _ = conn.Close(); _ = db.Close() }()

	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("FROM qa_records q")).
		WithArgs(start, start.Add(time.Hour), start, "", 100).
		WillReturnRows(segmentRows().AddRow(
			"req-missing", nil, int64(1), nil, int64(2), nil, "anthropic", nil, "claude", nil,
			200, true, int64(10), false, 1, 2, "a", "b",
			nil, "file:///does/not/exist", nil, nil, "captured", start.Add(time.Minute),
		))

	scratch := t.TempDir()
	_, err := BuildSegment(context.Background(), conn, BuildInput{
		WindowStart: start,
		WindowEnd:   start.Add(time.Hour),
		SegmentKind: SegmentKindBase,
		BlobRoot:    t.TempDir(),
		ScratchRoot: scratch,
		PageSize:    100,
	})
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || integrity.Code != IntegrityMissingEvidence || integrity.RequestID != "req-missing" {
		t.Fatalf("BuildSegment() error=%v", err)
	}
	entries, readErr := os.ReadDir(scratch)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch retained partial files: %v", entries)
	}
}

func segmentTestDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *sql.Conn) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db, mock, conn
}

func segmentRows() *sqlmock.Rows { return sqlmock.NewRows(segmentColumns) }
