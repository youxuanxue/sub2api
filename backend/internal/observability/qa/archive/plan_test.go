//go:build unit

package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPlanSourceDeltaStreamsCommittedAndSourceIdentities(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	start := time.Date(2026, 8, 7, 1, 0, 0, 0, time.UTC)
	t1, t2, t3 := start.Add(time.Minute), start.Add(2*time.Minute), start.Add(3*time.Minute)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT created_at, request_id FROM qa_records")).
		WithArgs(start, start.Add(time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "request_id"}).
			AddRow(t1, "req-1").AddRow(t2, "req-2").AddRow(t3, "req-3"))

	identityDir := t.TempDir()
	base := writeIdentityFixture(t, identityDir, "base.jsonl", []RecordIdentity{
		{CreatedAt: t1, RequestID: "req-1"}, {CreatedAt: t2, RequestID: "req-2"},
	})
	orphan := writeIdentityFixture(t, identityDir, "committed-only.jsonl", []RecordIdentity{
		{CreatedAt: start.Add(4 * time.Minute), RequestID: "req-4"},
	})
	commit := VerifiedCommit{
		RecordCount: 3,
		Segments:    []VerifiedSegment{{IdentityPath: base, IdentityCount: 2}, {IdentityPath: orphan, IdentityCount: 1}},
	}

	plan, err := PlanSourceDelta(context.Background(), conn, Window{Start: start, End: start.Add(time.Hour)}, commit)
	if err != nil {
		t.Fatalf("PlanSourceDelta()=%v", err)
	}
	if plan.SourceRecordCount != 3 || plan.CommittedRecordCount != 3 || plan.DeltaRecordCount != 1 || plan.CommittedOnlyCount != 1 {
		t.Fatalf("plan=%+v", plan)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func writeIdentityFixture(t *testing.T, dir, name string, identities []RecordIdentity) string {
	t.Helper()
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for _, identity := range identities {
		if err := encoder.Encode(identity); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
