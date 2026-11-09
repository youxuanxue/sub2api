package migrations

import (
	"strings"
	"testing"
)

// QA S3 is retired (2026-11-08). tk_110 must drop only the S3-only tables and
// never qa_archive_shards, which the local 24h cleanup still writes.
func TestMigrationTK110DropsOnlyQAArchiveS3Tables(t *testing.T) {
	t.Parallel()
	content, err := FS.ReadFile("tk_110_drop_qa_archive_s3_tables.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS qa_archive_segment_records;",
		"DROP TABLE IF EXISTS qa_archive_segments;",
		"DROP TABLE IF EXISTS qa_archive_gap_decision_receipts;",
		"DROP FUNCTION IF EXISTS tk_qa_archive_gap_decision_receipts_immutable();",
	} {
		if !strings.Contains(sql, stmt) {
			t.Fatalf("missing idempotent statement %q", stmt)
		}
	}
	if strings.Index(sql, "DROP TABLE IF EXISTS qa_archive_segment_records;") >
		strings.Index(sql, "DROP TABLE IF EXISTS qa_archive_segments;") {
		t.Fatal("child table qa_archive_segment_records must be dropped before qa_archive_segments")
	}
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if strings.Contains(strings.ToUpper(trimmed), "CASCADE") {
			t.Fatalf("drop must not use CASCADE: %q", trimmed)
		}
		if strings.Contains(trimmed, "qa_archive_shards") {
			t.Fatalf("tk_110 must not touch qa_archive_shards: %q", trimmed)
		}
	}
	if !strings.Contains(sql, "bluegreen-safe-destructive-ok:") {
		t.Fatal("destructive migration needs the blue/green acknowledgement")
	}
}
