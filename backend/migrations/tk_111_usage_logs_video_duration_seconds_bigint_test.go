package migrations

import (
	"strings"
	"testing"
)

// tk_025 (BIGINT) and upstream 172 (INTEGER) both ADD COLUMN IF NOT EXISTS
// video_duration_seconds, so whichever ran first decided the type; cn-prod
// ended up integer. tk_111 must widen only an integer column, never rewrite
// a database that is already bigint, and must not queue behind writers.
func TestMigrationTK111WidensOnlyAnIntegerVideoDuration(t *testing.T) {
	t.Parallel()
	content, err := FS.ReadFile("tk_111_usage_logs_video_duration_seconds_bigint.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	guard := strings.Index(sql, ") = 'integer' THEN")
	alter := strings.Index(sql, "ALTER TABLE usage_logs ALTER COLUMN video_duration_seconds TYPE BIGINT;")
	lock := strings.Index(sql, "SET LOCAL lock_timeout = '5s';")
	if guard < 0 || alter < 0 || lock < 0 {
		t.Fatalf("missing integer guard, lock_timeout or ALTER (guard=%d lock=%d alter=%d)", guard, lock, alter)
	}
	if guard >= lock || lock >= alter {
		t.Fatal("the ALTER must run only inside the integer guard, after lock_timeout is set")
	}
	if !strings.Contains(sql, "bluegreen-safe-destructive-ok:") {
		t.Fatal("ALTER COLUMN TYPE needs the blue/green acknowledgement")
	}
}
