//go:build unit

package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestShardRelativePrefix(t *testing.T) {
	start := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	if got := ShardRelativePrefix(start); got != "date=2026-08-06/hour=09" {
		t.Fatalf("ShardRelativePrefix()=%q", got)
	}
}

func TestUploadBaseSegmentWritesCommitAndManifest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New()=%v", err)
	}
	defer func() { _ = db.Close() }()

	windowStart := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(time.Hour)
	blobRoot := t.TempDir()
	blobPath := filepath.Join(blobRoot, "req.json.zst")
	if err := os.WriteFile(blobPath, []byte("payload"), 0o644); err != nil {
		t.Fatalf("WriteFile()=%v", err)
	}
	fileURI := "file://" + blobPath

	rows := sqlmock.NewRows([]string{
		"request_id", "trajectory_id", "user_id", "group_id", "api_key_id", "account_id",
		"platform", "provider", "requested_model", "upstream_model", "status_code", "success",
		"duration_ms", "stream", "input_tokens", "output_tokens",
		"request_sha256", "response_sha256",
		"blob_uri", "request_blob_uri", "response_blob_uri", "stream_blob_uri",
		"capture_status", "created_at",
	}).AddRow(
		"req-1", nil, int64(1), nil, int64(2), nil,
		"anthropic", nil, "claude", nil, 200, true,
		int64(10), false, 1, 2,
		"abc", "def",
		nil, fileURI, nil, nil,
		"captured", windowStart.Add(5*time.Minute),
	)
	mock.ExpectQuery("SELECT request_id").WithArgs(windowStart, windowEnd).WillReturnRows(rows)
	mock.ExpectExec("UPDATE qa_archive_shards SET").
		WithArgs(StateCommitted, int64(1), int64(1), int64(1), int64(0), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), windowStart).
		WillReturnResult(sqlmock.NewResult(0, 1))

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn()=%v", err)
	}
	defer func() { _ = conn.Close() }()

	store := NewMemoryObjectStore()
	result, err := UploadBaseSegment(context.Background(), conn, store, UploadInput{
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		BlobRoot:    blobRoot,
	})
	if err != nil {
		t.Fatalf("UploadBaseSegment()=%v", err)
	}
	if result.RecordCount != 1 || result.BlobPresentCount != 1 || result.BlobMissingCount != 0 {
		t.Fatalf("result=%+v", result)
	}
	keys := store.Keys()
	foundCommit := false
	foundParquet := false
	for _, key := range keys {
		if strings.HasSuffix(key, "commit.json") {
			foundCommit = true
		}
		if strings.HasSuffix(key, "records.parquet") {
			foundParquet = true
		}
	}
	if !foundCommit || !foundParquet {
		t.Fatalf("keys=%v", keys)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
