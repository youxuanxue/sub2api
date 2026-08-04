package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/partitionmaintenance"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
)

const (
	partitionMaintenanceConfirmation   = "tokenkey-prod-partition-maintenance-v1"
	partitionMaintenanceReceiptVersion = 1
	partitionMaintenanceReceiptMode    = "partition_maintenance"
)

type partitionMaintenanceDeps struct {
	loadConfig     func() (*config.Config, error)
	openDB         func(driverName, dataSourceName string) (*sql.DB, error)
	ensure         func(context.Context, pgpartition.DB, time.Time, partitionmaintenance.Mode) (partitionmaintenance.Result, error)
	writeHeartbeat func(context.Context, *sql.DB, *service.OpsUpsertJobHeartbeatInput) error
	now            func() time.Time
}

func defaultPartitionMaintenanceDeps() partitionMaintenanceDeps {
	return partitionMaintenanceDeps{
		loadConfig: config.LoadForBootstrap,
		openDB:     sql.Open,
		ensure:     partitionmaintenance.Ensure,
		writeHeartbeat: func(ctx context.Context, db *sql.DB, input *service.OpsUpsertJobHeartbeatInput) error {
			return repository.NewOpsRepository(db).UpsertJobHeartbeat(ctx, input)
		},
		now: time.Now,
	}
}

func (d partitionMaintenanceDeps) withDefaults() partitionMaintenanceDeps {
	defaults := defaultPartitionMaintenanceDeps()
	if d.loadConfig == nil {
		d.loadConfig = defaults.loadConfig
	}
	if d.openDB == nil {
		d.openDB = defaults.openDB
	}
	if d.ensure == nil {
		d.ensure = defaults.ensure
	}
	if d.writeHeartbeat == nil {
		d.writeHeartbeat = defaults.writeHeartbeat
	}
	if d.now == nil {
		d.now = defaults.now
	}
	return d
}

func partitionMaintenanceRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--partition-maintenance-once" || arg == "--partition-maintenance-once=true" {
			return true
		}
	}
	return false
}

func runPartitionMaintenanceCommand(
	ctx context.Context,
	args []string,
	out io.Writer,
	deps partitionMaintenanceDeps,
) error {
	fs := flag.NewFlagSet("partition-maintenance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var once bool
	var confirmation string
	fs.BoolVar(&once, "partition-maintenance-once", false, "run partition maintenance and exit")
	fs.StringVar(&confirmation, "confirm", "", "exact production maintenance confirmation")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("partition maintenance flags: %w", err)
	}
	if !once {
		return fmt.Errorf("partition maintenance mode was not requested")
	}
	if confirmation != partitionMaintenanceConfirmation {
		return fmt.Errorf("partition maintenance confirmation mismatch")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("partition maintenance does not accept positional arguments")
	}

	deps = deps.withDefaults()
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load partition maintenance config: %w", err)
	}
	db, err := deps.openDB("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return fmt.Errorf("open partition maintenance database: %w", err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping partition maintenance database: %w", err)
	}
	if _, err := db.ExecContext(ctx, "SET lock_timeout = '100ms'"); err != nil {
		return fmt.Errorf("set partition maintenance lock timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "SET statement_timeout = '5s'"); err != nil {
		return fmt.Errorf("set partition maintenance statement timeout: %w", err)
	}

	startedAt := deps.now().UTC()
	result, err := deps.ensure(
		ctx,
		db,
		startedAt,
		partitionmaintenance.ModeRequireAllPartitioned,
	)
	if err != nil {
		return fmt.Errorf("ensure production partitions: %w", err)
	}
	completedAt := deps.now().UTC()
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}
	durationMs := duration.Milliseconds()
	lastResult := "tables=" + result.String() + " deletion_authorized=false"
	if err := deps.writeHeartbeat(ctx, db, &service.OpsUpsertJobHeartbeatInput{
		JobName:        partitionmaintenance.JobName,
		LastRunAt:      &startedAt,
		LastSuccessAt:  &completedAt,
		LastDurationMs: &durationMs,
		LastResult:     &lastResult,
	}); err != nil {
		return fmt.Errorf("write partition maintenance heartbeat: %w", err)
	}

	receipt := struct {
		ReceiptVersion     int                                `json:"receipt_version"`
		Mode               string                             `json:"mode"`
		OK                 bool                               `json:"ok"`
		JobName            string                             `json:"job_name"`
		CompletedAt        time.Time                          `json:"completed_at"`
		Tables             []partitionmaintenance.TableResult `json:"tables"`
		DeletionAuthorized bool                               `json:"deletion_authorized"`
	}{
		ReceiptVersion:     partitionMaintenanceReceiptVersion,
		Mode:               partitionMaintenanceReceiptMode,
		OK:                 true,
		JobName:            partitionmaintenance.JobName,
		CompletedAt:        completedAt,
		Tables:             result.Tables,
		DeletionAuthorized: false,
	}
	if err := json.NewEncoder(out).Encode(receipt); err != nil {
		return fmt.Errorf("encode partition maintenance receipt: %w", err)
	}
	return nil
}
