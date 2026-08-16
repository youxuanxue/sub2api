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
