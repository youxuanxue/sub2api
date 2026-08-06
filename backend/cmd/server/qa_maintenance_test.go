//go:build unit

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestQAMaintenanceRejectsWrongConfirmation(t *testing.T) {
	err := runQAMaintenanceCommand(
		context.Background(),
		[]string{"--qa-maintenance-once", "--confirm", "wrong"},
		&bytes.Buffer{},
		qaMaintenanceDeps{},
	)
	if err == nil || err.Error() != "qa maintenance confirmation mismatch" {
		t.Fatalf("err=%v", err)
	}
}

func TestDefaultQAMaintenancePlanShardUpsertsControlRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New()=%v", err)
	}
	defer func() { _ = db.Close() }()

	runAt := time.Date(2026, 8, 6, 10, 15, 0, 0, time.UTC)
	windowStart := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	windowEnd := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT COUNT\\(\\*\\)").
		WithArgs(windowStart, windowEnd).
		WillReturnRows(sqlmock.NewRows([]string{"count", "blob_ref_count"}).AddRow(42, 7))
	mock.ExpectExec("INSERT INTO qa_archive_shards").
		WithArgs(windowStart, windowEnd, "pending", int64(42), int64(7), "raw/v1/date=2026-08-06/hour=09", runAt).
		WillReturnResult(sqlmock.NewResult(1, 1))

	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("db.Conn()=%v", err)
	}
	defer func() { _ = conn.Close() }()

	plan, err := defaultQAMaintenancePlanShard(
		context.Background(),
		conn,
		runAt,
		"raw/v1/date=2026-08-06/hour=09",
		false,
	)
	if err != nil {
		t.Fatalf("defaultQAMaintenancePlanShard()=%v", err)
	}
	if plan.RecordCount != 42 || plan.BlobRefCount != 7 || plan.ArchiveEnabled {
		t.Fatalf("plan=%+v", plan)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestQAMaintenanceReceiptShape(t *testing.T) {
	receipt := struct {
		DeletionAuthorized bool   `json:"deletion_authorized"`
		UploadAuthorized   bool   `json:"upload_authorized"`
		Mode               string `json:"mode"`
	}{
		DeletionAuthorized: false,
		UploadAuthorized:   false,
		Mode:               qaMaintenanceReceiptMode,
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("Marshal()=%v", err)
	}
	if !bytes.Contains(raw, []byte(`"deletion_authorized":false`)) {
		t.Fatalf("raw=%s", raw)
	}
}
