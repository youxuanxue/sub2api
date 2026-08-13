//go:build unit

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/partitionmaintenance"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestPartitionMaintenanceRejectsWrongConfirmationBeforeDependencies(t *testing.T) {
	loadCalled := false
	openCalled := false
	deps := partitionMaintenanceDeps{
		loadConfig: func() (*config.Config, error) {
			loadCalled = true
			return nil, nil
		},
		openDB: func(string, string) (*sql.DB, error) {
			openCalled = true
			return nil, nil
		},
	}

	err := runPartitionMaintenanceCommand(
		context.Background(),
		[]string{"--partition-maintenance-once", "--confirm", "wrong"},
		&bytes.Buffer{},
		deps,
	)
	if err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	if loadCalled || openCalled {
		t.Fatalf("wrong confirmation touched dependencies: load=%v open=%v", loadCalled, openCalled)
	}
}

func TestPartitionMaintenanceSuccessUsesStrictBoundedPath(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	mock.ExpectPing()
	mock.ExpectExec("SET lock_timeout = '100ms'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SET statement_timeout = '5s'").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectClose()

	fixedNow := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	loadCalls := 0
	openCalls := 0
	ensureCalls := 0
	var heartbeat *service.OpsUpsertJobHeartbeatInput
	deps := partitionMaintenanceDeps{
		loadConfig: func() (*config.Config, error) {
			loadCalls++
			return &config.Config{
				Timezone: "UTC",
				Database: config.DatabaseConfig{
					Host: "postgres", Port: 5432, User: "tokenkey", DBName: "tokenkey", SSLMode: "disable",
				},
			}, nil
		},
		openDB: func(driverName, dsn string) (*sql.DB, error) {
			openCalls++
			if driverName != "postgres" || !strings.Contains(dsn, "TimeZone=UTC") {
				t.Fatalf("unexpected open args: driver=%q dsn=%q", driverName, dsn)
			}
			return db, nil
		},
		ensure: func(_ context.Context, gotDB pgpartition.DB, now time.Time, mode partitionmaintenance.Mode, _ partitionmaintenance.Options) (partitionmaintenance.Result, error) {
			ensureCalls++
			if _, ok := gotDB.(*sql.Conn); !ok || !now.Equal(fixedNow) || mode != partitionmaintenance.ModeRequireAllPartitioned {
				t.Fatalf("unexpected ensure args: db=%T now=%s mode=%d", gotDB, now, mode)
			}
			return partitionmaintenance.Result{Tables: []partitionmaintenance.TableResult{
				{Table: "ops_system_logs", RangeCount: 8},
				{Table: "ops_error_logs", RangeCount: 8},
				{Table: "usage_logs", RangeCount: 8},
			}}, nil
		},
		writeHeartbeat: func(heartbeatCtx context.Context, gotDB *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
			if gotDB != db {
				t.Fatalf("heartbeat db=%p want %p", gotDB, db)
			}
			deadline, ok := heartbeatCtx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining <= 0 || remaining > 5*time.Second {
				t.Fatalf("heartbeat context must have a live bounded deadline: ok=%v remaining=%s", ok, remaining)
			}
			heartbeat = input
			return nil
		},
		now: func() time.Time { return fixedNow },
	}

	out := &bytes.Buffer{}
	err = runPartitionMaintenanceCommand(
		context.Background(),
		[]string{"--partition-maintenance-once", "--confirm", partitionMaintenanceConfirmation},
		out,
		deps,
	)
	if err != nil {
		t.Fatalf("runPartitionMaintenanceCommand: %v", err)
	}
	if loadCalls != 1 || openCalls != 1 || ensureCalls != 1 {
		t.Fatalf("calls load=%d open=%d ensure=%d", loadCalls, openCalls, ensureCalls)
	}
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatalf("max open connections=%d want 1", db.Stats().MaxOpenConnections)
	}
	if heartbeat == nil || heartbeat.JobName != partitionmaintenance.JobName || heartbeat.LastSuccessAt == nil {
		t.Fatalf("invalid heartbeat: %+v", heartbeat)
	}
	if heartbeat.LastResult == nil || !strings.Contains(*heartbeat.LastResult, "deletion_authorized=false") {
		t.Fatalf("heartbeat result must deny deletion: %+v", heartbeat)
	}
	var receipt struct {
		ReceiptVersion     int                                `json:"receipt_version"`
		Mode               string                             `json:"mode"`
		OK                 bool                               `json:"ok"`
		JobName            string                             `json:"job_name"`
		Tables             []partitionmaintenance.TableResult `json:"tables"`
		DeletionAuthorized bool                               `json:"deletion_authorized"`
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatalf("decode output %q: %v", out.String(), err)
	}
	if receipt.ReceiptVersion != 1 || receipt.Mode != "partition_maintenance" || !receipt.OK || receipt.JobName != partitionmaintenance.JobName || receipt.DeletionAuthorized || len(receipt.Tables) != 3 {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("database expectations: %v", err)
	}
}

func TestPartitionMaintenanceMainDispatchPrecedesSetupAndServer(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	maintenanceAt := bytes.Index(body, []byte("partitionMaintenanceRequested(os.Args[1:])"))
	setupAt := bytes.Index(body, []byte("setup.NeedsSetup()"))
	serverAt := bytes.Index(body, []byte("runMainServer()"))
	if maintenanceAt < 0 || setupAt < 0 || serverAt < 0 || maintenanceAt >= setupAt || maintenanceAt >= serverAt {
		t.Fatalf("maintenance dispatch must precede setup/server: maintenance=%d setup=%d server=%d", maintenanceAt, setupAt, serverAt)
	}
}
