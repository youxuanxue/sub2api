package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestIsQueryTimeoutErr(t *testing.T) {
	if !isQueryTimeoutErr(context.DeadlineExceeded) {
		t.Fatalf("context.DeadlineExceeded should be treated as query timeout")
	}
	if !isQueryTimeoutErr(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)) {
		t.Fatalf("wrapped context.DeadlineExceeded should be treated as query timeout")
	}
	if !isQueryTimeoutErr(errors.New("pq: canceling statement due to user request")) {
		t.Fatalf("lib/pq cancel-after-deadline should be treated as query timeout")
	}
	if isQueryTimeoutErr(errors.New("pq: canceling statement due to statement timeout")) {
		t.Fatalf("Postgres statement timeout is not the local peak/latency deadline")
	}
	if isQueryTimeoutErr(context.Canceled) {
		t.Fatalf("context.Canceled should not be treated as query timeout")
	}
	if isQueryTimeoutErr(fmt.Errorf("wrapped: %w", context.Canceled)) {
		t.Fatalf("wrapped context.Canceled should not be treated as query timeout")
	}
	if isQueryTimeoutErr(nil) {
		t.Fatalf("nil should not be treated as query timeout")
	}
}

func TestSkipRawPeakScan(t *testing.T) {
	end := time.Date(2026, 8, 20, 15, 31, 0, 0, time.UTC)
	if skipRawPeakScan(end.Add(-time.Hour), end) {
		t.Fatalf("1h window should still use the raw minute-bucket peak scan")
	}
	if skipRawPeakScan(end.Add(-2*time.Hour), end) {
		t.Fatalf("exactly 2h should still use the raw minute-bucket peak scan")
	}
	if !skipRawPeakScan(end.Add(-30*24*time.Hour), end) {
		t.Fatalf("30d custom window must skip the raw peak scan")
	}
}
