//go:build unit

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	qaobs "github.com/Wei-Shaw/sub2api/internal/observability/qa"
)

func TestQABundleCanaryCommandUsesCanonicalRunnerAndReceipt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectClose()
	want := qaobs.BundleCanaryReceipt{
		SchemaVersion: "qa-bundle-canary-v1", OK: true, JobID: "job",
		ArchiveWatermark: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC), CommitCount: 24,
	}
	out := &bytes.Buffer{}
	err = runQABundleCanaryCommand(context.Background(), []string{
		"--qa-bundle-canary", "--confirm", qaBundleCanaryConfirmation, "--timeout-seconds", "30",
	}, out, qaBundleCanaryDeps{
		loadConfig: func() (*config.Config, error) { return &config.Config{}, nil },
		openDB:     func(string, string) (*sql.DB, error) { return db, nil },
		run: func(context.Context, *config.Config, *sql.DB, time.Duration) (qaobs.BundleCanaryReceipt, error) {
			return want, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got qaobs.BundleCanaryReceipt
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.JobID != want.JobID || got.CommitCount != 24 {
		t.Fatalf("receipt=%+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQABundleCanaryCommandDefaultTimeoutAndValidation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectClose()

	var capturedTimeout time.Duration
	out := &bytes.Buffer{}
	err = runQABundleCanaryCommand(context.Background(), []string{
		"--qa-bundle-canary", "--confirm", qaBundleCanaryConfirmation,
	}, out, qaBundleCanaryDeps{
		loadConfig: func() (*config.Config, error) { return &config.Config{}, nil },
		openDB:     func(string, string) (*sql.DB, error) { return db, nil },
		run: func(ctx context.Context, cfg *config.Config, d *sql.DB, timeout time.Duration) (qaobs.BundleCanaryReceipt, error) {
			capturedTimeout = timeout
			return qaobs.BundleCanaryReceipt{SchemaVersion: "qa-bundle-canary-v1", OK: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedTimeout != 1800*time.Second {
		t.Fatalf("expected default timeout 1800s, got %v", capturedTimeout)
	}

	// Boundary: 3600 is valid
	err = runQABundleCanaryCommand(context.Background(), []string{
		"--qa-bundle-canary", "--confirm", qaBundleCanaryConfirmation, "--timeout-seconds", "3600",
	}, out, qaBundleCanaryDeps{
		loadConfig: func() (*config.Config, error) { return &config.Config{}, nil },
		openDB:     func(string, string) (*sql.DB, error) { return db, nil },
		run: func(ctx context.Context, cfg *config.Config, d *sql.DB, timeout time.Duration) (qaobs.BundleCanaryReceipt, error) {
			capturedTimeout = timeout
			return qaobs.BundleCanaryReceipt{SchemaVersion: "qa-bundle-canary-v1", OK: true}, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedTimeout != 3600*time.Second {
		t.Fatalf("expected timeout 3600s, got %v", capturedTimeout)
	}

	// Boundary: > 3600 is rejected
	err = runQABundleCanaryCommand(context.Background(), []string{
		"--qa-bundle-canary", "--confirm", qaBundleCanaryConfirmation, "--timeout-seconds", "3601",
	}, out, qaBundleCanaryDeps{})
	if err == nil {
		t.Fatal("expected error for timeout > 3600")
	}

	// Boundary: <= 0 is rejected
	err = runQABundleCanaryCommand(context.Background(), []string{
		"--qa-bundle-canary", "--confirm", qaBundleCanaryConfirmation, "--timeout-seconds", "0",
	}, out, qaBundleCanaryDeps{})
	if err == nil {
		t.Fatal("expected error for timeout <= 0")
	}
}
