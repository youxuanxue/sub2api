//go:build unit

package pgpartition

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
)

// execRecorder is a minimal pgpartition.DB that records ExecContext queries and can be
// scripted to return an error for the Nth call (e.g. a 42P17 overlap). QueryContext /
// QueryRowContext are unused by EnsureMonthly and panic if reached.
type execRecorder struct {
	queries []string
	errs    []error // errs[i] returned for the (i+1)th ExecContext call; nil/out-of-range = success
}

func (r *execRecorder) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	idx := len(r.queries)
	r.queries = append(r.queries, query)
	if idx < len(r.errs) && r.errs[idx] != nil {
		return nil, r.errs[idx]
	}
	return driverResult{}, nil
}

func (r *execRecorder) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	panic("QueryContext not expected in EnsureMonthly")
}
func (r *execRecorder) QueryRowContext(context.Context, string, ...any) *sql.Row {
	panic("QueryRowContext not expected in EnsureMonthly")
}

type driverResult struct{}

func (driverResult) LastInsertId() (int64, error) { return 0, nil }
func (driverResult) RowsAffected() (int64, error) { return 0, nil }

func TestMonthStartUTC(t *testing.T) {
	in := time.Date(2026, 6, 20, 13, 45, 7, 0, time.FixedZone("x", 8*3600))
	got := monthStartUTC(in)
	want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("monthStartUTC=%s want %s", got, want)
	}
}

func TestIsOverlap(t *testing.T) {
	if !isOverlap(&pq.Error{Code: pq.ErrorCode(pgPartitionOverlapCode)}) {
		t.Fatal("42P17 must be detected as overlap")
	}
	if isOverlap(&pq.Error{Code: "23505"}) {
		t.Fatal("non-overlap pq error must not be treated as overlap")
	}
	if isOverlap(errors.New("plain error")) {
		t.Fatal("non-pq error must not be treated as overlap")
	}
}

func TestEnsureMonthly_CreatesCurrentThroughAhead(t *testing.T) {
	rec := &execRecorder{}
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := EnsureMonthly(context.Background(), rec, "ops_system_logs", "created_at", now, 2); err != nil {
		t.Fatalf("EnsureMonthly: %v", err)
	}
	// monthsAhead=2 -> current + 2 future = 3 CREATE statements, June/July/Aug 2026.
	if len(rec.queries) != 3 {
		t.Fatalf("expected 3 CREATE statements, got %d: %v", len(rec.queries), rec.queries)
	}
	wantNames := []string{"ops_system_logs_202606", "ops_system_logs_202607", "ops_system_logs_202608"}
	wantBounds := [][2]string{{"2026-06-01", "2026-07-01"}, {"2026-07-01", "2026-08-01"}, {"2026-08-01", "2026-09-01"}}
	for i, q := range rec.queries {
		if !strings.Contains(q, wantNames[i]) {
			t.Errorf("statement %d missing partition name %s: %s", i, wantNames[i], q)
		}
		if !strings.Contains(q, "PARTITION OF "+pq.QuoteIdentifier("ops_system_logs")) {
			t.Errorf("statement %d not a PARTITION OF ops_system_logs: %s", i, q)
		}
		if !strings.Contains(q, wantBounds[i][0]) || !strings.Contains(q, wantBounds[i][1]) {
			t.Errorf("statement %d bounds want %v: %s", i, wantBounds[i], q)
		}
	}
}

func TestEnsureMonthly_SkipsOverlapContinues(t *testing.T) {
	// First CREATE (current month) overlaps the legacy partition -> 42P17; must be
	// skipped and the remaining future months still created.
	rec := &execRecorder{errs: []error{&pq.Error{Code: pq.ErrorCode(pgPartitionOverlapCode)}}}
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := EnsureMonthly(context.Background(), rec, "ops_system_logs", "created_at", now, 2); err != nil {
		t.Fatalf("overlap on month 0 must be benign, got: %v", err)
	}
	if len(rec.queries) != 3 {
		t.Fatalf("must still attempt all 3 months, got %d", len(rec.queries))
	}
}

func TestEnsureMonthly_RealErrorPropagates(t *testing.T) {
	rec := &execRecorder{errs: []error{&pq.Error{Code: "53100"}}} // disk full -> must fail
	now := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := EnsureMonthly(context.Background(), rec, "ops_system_logs", "created_at", now, 2); err == nil {
		t.Fatal("a non-overlap error must propagate")
	}
}
