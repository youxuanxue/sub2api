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
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	_ "github.com/lib/pq"
)

const (
	restoreConfirmationPrefix        = "tokenkey-prod-qa-archive-restore-v1"
	forwardCutoverConfirmationPrefix = "tokenkey-prod-qa-forward-cutover-v1"
)

type controlStatus struct {
	Exists                bool   `json:"exists"`
	State                 string `json:"state,omitempty"`
	CommitETag            string `json:"commit_etag,omitempty"`
	AggregateRecordCount  int64  `json:"aggregate_record_count"`
	AggregateBlobRefCount int64  `json:"aggregate_blob_ref_count"`
	VerificationErrorCode string `json:"verification_error_code,omitempty"`
	CleanupEligible       bool   `json:"cleanup_eligible"`
}

type cliDeps struct {
	loadConfig        func() (*config.Config, error)
	openDB            func(string, string) (*sql.DB, error)
	newObjectStore    func(context.Context, config.QACaptureStorageConfig) (archive.ObjectStore, error)
	verifyCommit      func(context.Context, archive.ObjectStore, string, string) (archive.VerifiedCommit, error)
	inspectControl    func(context.Context, *sql.Conn, archive.Window) (controlStatus, error)
	planSourceDelta   func(context.Context, *sql.Conn, archive.Window, archive.VerifiedCommit) (archive.SourceDeltaPlan, error)
	setForwardCutover func(context.Context, *sql.Conn) (archive.ForwardCutover, error)
}

func defaultDeps() cliDeps {
	return cliDeps{
		loadConfig:        config.LoadForBootstrap,
		openDB:            sql.Open,
		newObjectStore:    archive.NewObjectStoreFromConfig,
		verifyCommit:      archive.VerifyCommit,
		inspectControl:    defaultInspectControl,
		planSourceDelta:   archive.PlanSourceDelta,
		setForwardCutover: archive.NewSQLControlStore().SetApprovedForwardCutover,
	}
}

func main() {
	if err := runCLI(context.Background(), os.Args[1:], os.Stdout, defaultDeps()); err != nil {
		_ = writeJSON(os.Stderr, map[string]any{
			"ok": false, "error": err.Error(), "deletion_authorized": false,
		})
		os.Exit(2)
	}
}

func runCLI(ctx context.Context, args []string, out io.Writer, deps cliDeps) error {
	if len(args) == 0 {
		return fmt.Errorf("command required: inspect, verify, restore, repair-plan, or set-forward-cutover")
	}
	switch args[0] {
	case "inspect", "verify", "restore", "repair-plan":
		return runWindowCommand(ctx, args[0], args[1:], out, deps)
	case "set-forward-cutover":
		return runSetForwardCutover(ctx, args[1:], out, deps)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runSetForwardCutover(ctx context.Context, args []string, out io.Writer, deps cliDeps) error {
	fs := flag.NewFlagSet("set-forward-cutover", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	confirm := fs.String("confirm", "", "exact approved cutover confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	approved := archive.Phase2ForwardCutoverWindow()
	if *confirm != windowConfirmation(forwardCutoverConfirmationPrefix, approved.Start) {
		return fmt.Errorf("exact cutover confirmation required")
	}
	deps = fillDefaults(deps)
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	db, conn, err := openCLIConnection(ctx, deps, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(); _ = db.Close() }()
	cutover, err := deps.setForwardCutover(ctx, conn)
	if err != nil {
		return fmt.Errorf("set approved forward cutover: %w", err)
	}
	if !cutover.Window.Start.Equal(approved.Start) || !cutover.Window.End.Equal(approved.End) {
		return fmt.Errorf("set approved forward cutover returned an unexpected window")
	}
	return writeJSON(out, map[string]any{
		"ok": true, "command": "set-forward-cutover", "shard_id": cutover.ShardID,
		"window_start": cutover.Window.Start, "window_end": cutover.Window.End,
		"restore_verified_at": cutover.RestoreVerifiedAt,
		"forward_cutover":     true, "deletion_authorized": false,
	})
}

func runWindowCommand(ctx context.Context, command string, args []string, out io.Writer, deps cliDeps) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	windowArg := fs.String("window-start", "", "UTC hour in RFC3339 format")
	outputDir := fs.String("output", "", "new restore directory")
	confirm := fs.String("confirm", "", "window-bound confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	window, err := parseWindow(*windowArg)
	if err != nil {
		return err
	}

	if command == "restore" {
		if *confirm != windowConfirmation(restoreConfirmationPrefix, window.Start) {
			return fmt.Errorf("window-bound privacy confirmation required")
		}
		validatedOutput, err := validateRestoreOutput(*outputDir)
		if err != nil {
			return err
		}
		*outputDir = validatedOutput
	}
	deps = fillDefaults(deps)
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	store, err := deps.newObjectStore(ctx, cfg.QaArchive.Storage)
	if err != nil {
		return fmt.Errorf("open archive store: %w", err)
	}
	commitKey := archive.ShardRelativePrefix(window.Start) + "/commit.json"

	switch command {
	case "inspect":
		return runInspect(ctx, out, deps, cfg, store, window, commitKey)
	case "verify":
		verified, err := deps.verifyCommit(ctx, store, commitKey, "")
		if err != nil {
			return fmt.Errorf("verify archive commit: %w", err)
		}
		defer func() { _ = verified.Close() }()
		return writeJSON(out, verifiedReceipt(command, commitKey, verified))
	case "restore":
		if _, err := os.Lstat(*outputDir); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("restore output already exists")
			}
			return fmt.Errorf("inspect restore output: %w", err)
		}
		verified, err := deps.verifyCommit(ctx, store, commitKey, filepath.Clean(*outputDir))
		if err != nil {
			return fmt.Errorf("restore archive commit: %w", err)
		}
		defer func() { _ = verified.Close() }()
		receipt := verifiedReceipt(command, commitKey, verified)
		receipt["restore_dir"] = filepath.Clean(*outputDir)
		receipt["privacy_confirmed"] = true
		return writeJSON(out, receipt)
	case "repair-plan":
		return runRepairPlan(ctx, out, deps, cfg, store, window, commitKey)
	}
	return fmt.Errorf("unsupported command %q", command)
}

func runInspect(ctx context.Context, out io.Writer, deps cliDeps, cfg *config.Config, store archive.ObjectStore, window archive.Window, commitKey string) error {
	db, conn, err := openCLIConnection(ctx, deps, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(); _ = db.Close() }()
	control, err := deps.inspectControl(ctx, conn, window)
	if err != nil {
		return fmt.Errorf("inspect archive control: %w", err)
	}
	verified, verifyErr := deps.verifyCommit(ctx, store, commitKey, "")
	defer func() { _ = verified.Close() }()
	receipt := map[string]any{
		"ok": true, "command": "inspect", "window_start": window.Start, "window_end": window.End,
		"control": control, "verified": verifyErr == nil, "blocked": false,
		"cleanup_eligible": false, "deletion_authorized": false,
	}
	if verifyErr == nil {
		for key, value := range verifiedReceipt("inspect", commitKey, verified) {
			receipt[key] = value
		}
		return writeJSON(out, receipt)
	}
	var integrity *archive.IntegrityError
	if !errors.As(verifyErr, &integrity) {
		return fmt.Errorf("inspect archive commit: %w", verifyErr)
	}
	receipt["verification_error_code"] = integrity.Code
	receipt["blocked"] = isBlockedCode(integrity.Code)
	if integrity.RequestID != "" {
		receipt["request_id"] = integrity.RequestID
	}
	return writeJSON(out, receipt)
}

func runRepairPlan(ctx context.Context, out io.Writer, deps cliDeps, cfg *config.Config, store archive.ObjectStore, window archive.Window, commitKey string) error {
	db, conn, err := openCLIConnection(ctx, deps, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(); _ = db.Close() }()
	verified, exists, err := readOptionalCommit(ctx, deps, store, commitKey)
	if err != nil {
		return err
	}
	defer func() { _ = verified.Close() }()
	plan, err := deps.planSourceDelta(ctx, conn, window, verified)
	if err != nil {
		return fmt.Errorf("plan source delta: %w", err)
	}
	control, err := deps.inspectControl(ctx, conn, window)
	if err != nil {
		return fmt.Errorf("inspect archive control: %w", err)
	}
	controlMismatch := control.Exists != exists || (control.Exists && exists &&
		(control.CommitETag != verified.ETag || control.AggregateRecordCount != verified.RecordCount ||
			control.AggregateBlobRefCount != verified.BlobRefCount || control.CleanupEligible))
	return writeJSON(out, map[string]any{
		"ok": true, "command": "repair-plan", "window_start": window.Start,
		"window_end": window.End, "commit_exists": exists, "commit_etag": verified.ETag,
		"source_record_count": plan.SourceRecordCount, "committed_record_count": plan.CommittedRecordCount,
		"delta_record_count": plan.DeltaRecordCount, "committed_only_count": plan.CommittedOnlyCount,
		"control": control, "control_mismatch": controlMismatch,
		"would_write_delta": plan.DeltaRecordCount > 0,
		"cleanup_eligible":  false, "deletion_authorized": false,
	})
}

func fillDefaults(deps cliDeps) cliDeps {
	defaults := defaultDeps()
	if deps.loadConfig == nil {
		deps.loadConfig = defaults.loadConfig
	}
	if deps.openDB == nil {
		deps.openDB = defaults.openDB
	}
	if deps.newObjectStore == nil {
		deps.newObjectStore = defaults.newObjectStore
	}
	if deps.verifyCommit == nil {
		deps.verifyCommit = defaults.verifyCommit
	}
	if deps.inspectControl == nil {
		deps.inspectControl = defaults.inspectControl
	}
	if deps.planSourceDelta == nil {
		deps.planSourceDelta = defaults.planSourceDelta
	}
	if deps.setForwardCutover == nil {
		deps.setForwardCutover = defaults.setForwardCutover
	}
	return deps
}

func parseWindow(value string) (archive.Window, error) {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil || parsed.Location() != time.UTC || parsed.Second() != 0 || parsed.Nanosecond() != 0 || parsed.Minute() != 0 {
		return archive.Window{}, fmt.Errorf("--window-start must be an exact UTC RFC3339 hour")
	}
	return archive.Window{Start: parsed.UTC(), End: parsed.UTC().Add(time.Hour)}, nil
}

func validateRestoreOutput(value string) (string, error) {
	root, err := filepath.Abs(filepath.Join(dataDir(), "qa_archive_restore"))
	if err != nil {
		return "", fmt.Errorf("resolve isolated restore root: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create isolated restore root: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("isolated restore root is not a real directory")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", fmt.Errorf("secure isolated restore root: %w", err)
	}
	output, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("resolve restore output: %w", err)
	}
	relative, err := filepath.Rel(root, output)
	if err != nil || relative == "." || relative == ".." || strings.Contains(relative, string(filepath.Separator)) {
		return "", fmt.Errorf("restore output must be one new directory under isolated restore root")
	}
	return output, nil
}

func windowConfirmation(prefix string, start time.Time) string {
	return prefix + ":" + start.UTC().Format(time.RFC3339)
}

func openCLIConnection(ctx context.Context, deps cliDeps, cfg *config.Config) (*sql.DB, *sql.Conn, error) {
	db, err := deps.openDB("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("ping database: %w", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("acquire database connection: %w", err)
	}
	return db, conn, nil
}

func readOptionalCommit(ctx context.Context, deps cliDeps, store archive.ObjectStore, key string) (archive.VerifiedCommit, bool, error) {
	exists, err := store.Head(ctx, key)
	if err != nil {
		return archive.VerifiedCommit{}, false, fmt.Errorf("head archive commit: %w", err)
	}
	if !exists {
		return archive.VerifiedCommit{}, false, nil
	}
	verified, err := deps.verifyCommit(ctx, store, key, "")
	if err != nil {
		return archive.VerifiedCommit{}, true, fmt.Errorf("verify archive commit: %w", err)
	}
	return verified, true, nil
}

func defaultInspectControl(ctx context.Context, conn *sql.Conn, window archive.Window) (controlStatus, error) {
	var status controlStatus
	err := conn.QueryRowContext(ctx, `
SELECT state, COALESCE(commit_etag,''), aggregate_record_count,
       aggregate_blob_ref_count, COALESCE(verification_error_code,''), cleanup_eligible
FROM qa_archive_shards
WHERE window_start=$1 AND generation=0`, window.Start).Scan(
		&status.State, &status.CommitETag, &status.AggregateRecordCount,
		&status.AggregateBlobRefCount, &status.VerificationErrorCode, &status.CleanupEligible,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return controlStatus{}, nil
	}
	if err != nil {
		return controlStatus{}, err
	}
	status.Exists = true
	return status, nil
}

func isBlockedCode(code string) bool {
	switch code {
	case archive.IntegrityMissingEvidence, archive.IntegrityCorruptArtifact, "commit_mismatch", "restore_failed":
		return true
	default:
		return false
	}
}

func verifiedReceipt(command, commitKey string, verified archive.VerifiedCommit) map[string]any {
	return map[string]any{
		"ok": true, "command": command, "verified": true,
		"window_start": verified.Document.WindowStart, "window_end": verified.Document.WindowEnd,
		"commit_key": commitKey, "commit_etag": verified.ETag, "segment_count": len(verified.Document.Segments),
		"record_count": verified.RecordCount, "blob_ref_count": verified.BlobRefCount,
		"blob_present_count": verified.BlobPresentCount, "blob_missing_count": verified.BlobMissingCount,
		"cleanup_eligible": false, "deletion_authorized": false,
	}
}

func dataDir() string {
	if value := strings.TrimSpace(os.Getenv("DATA_DIR")); value != "" {
		return value
	}
	return "/app/data"
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(value)
}
