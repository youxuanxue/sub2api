// qa-local-maintenance runs local QA lifecycle phases without an object store.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/lifecycle"
	_ "github.com/lib/pq"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"ok": false, "error": err.Error()})
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("qa-local-maintenance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	phase := fs.String("phase", "", "local lifecycle phase")
	dryRun := fs.Bool("dry-run", false, "plan cleanup without deleting")
	confirm := fs.String("confirm", "", "exact phase confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*phase != "provision" && *phase != "cleanup") {
		return errors.New("phase must be provision or cleanup; no positional arguments")
	}
	if *confirm != "tokenkey-qa-local-"+*phase+"-v1" {
		return errors.New("phase confirmation mismatch")
	}
	// lib/pq consumes PG* environment variables supplied by the host runner.
	// No application config, cloud credentials, or archive client is loaded.
	db, err := sql.Open("postgres", "")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err = conn.ExecContext(ctx, "SET lock_timeout='100ms'; SET statement_timeout='120s'; SET timezone='UTC'; SET search_path=public"); err != nil {
		return err
	}
	if *dryRun {
		if _, err = conn.ExecContext(ctx, "SET default_transaction_read_only=on"); err != nil {
			return err
		}
	}
	var primary bool
	var partitionKey string
	if err = conn.QueryRowContext(ctx, "SELECT NOT pg_is_in_recovery(), pg_get_partkeydef('public.qa_records'::regclass)").Scan(&primary, &partitionKey); err != nil {
		return err
	}
	if !primary || partitionKey != "RANGE (created_at)" {
		return errors.New("expected primary QA hourly partition parent")
	}
	var locked bool
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lifecycle.MaintenanceLockID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errors.New("QA lifecycle advisory lock busy")
	}
	// The connection closes on every exit, releasing the session lock.
	started := time.Now().UTC()
	receipt := map[string]any{"schema_version": 1, "phase": *phase, "started_at": started, "upload_authorized": false, "deletion_authorized": false}
	if *phase == "provision" {
		if *dryRun {
			return errors.New("dry-run is only supported for cleanup")
		}
		var result lifecycle.ProvisionResult
		result, err = lifecycle.RunProvision(ctx, conn, lifecycle.Options{}, nil)
		receipt["provision"] = result
	} else {
		var result lifecycle.LocalCleanupResult
		result, err = lifecycle.RunLocalCleanup(ctx, conn, lifecycle.Options{
			BlobRoot: "/app/data", DLQRoot: "/app/data",
			// The host mounts the node1 receipt view read-only. Missing receipts
			// are a hard gate; this keeps the 24h policy from deleting the only copy.
			ArchiveReceiptRoot: os.Getenv("QA_NODE1_RECEIPT_ROOT"),
			ArchiveEnvironment: os.Getenv("QA_NODE1_ENVIRONMENT"),
		}, *dryRun)
		receipt["cleanup"] = result
		receipt["deletion_authorized"] = !*dryRun
	}
	receipt["ok"] = err == nil
	receipt["completed_at"] = time.Now().UTC()
	if err != nil {
		receipt["error"] = err.Error()
	}
	if encodeErr := json.NewEncoder(out).Encode(receipt); encodeErr != nil {
		return errors.Join(err, fmt.Errorf("write receipt: %w", encodeErr))
	}
	return err
}
