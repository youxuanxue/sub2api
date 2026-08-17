package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	qaobs "github.com/Wei-Shaw/sub2api/internal/observability/qa"
)

const qaBundleCanaryConfirmation = "tokenkey-prod-qa-bundle-canary-v1"

type qaBundleCanaryDeps struct {
	loadConfig func() (*config.Config, error)
	openDB     func(string, string) (*sql.DB, error)
	run        func(context.Context, *config.Config, *sql.DB, time.Duration) (qaobs.BundleCanaryReceipt, error)
}

func defaultQABundleCanaryDeps() qaBundleCanaryDeps {
	return qaBundleCanaryDeps{loadConfig: config.LoadForBootstrap, openDB: sql.Open, run: qaobs.RunBundleCanary}
}

func (d qaBundleCanaryDeps) withDefaults() qaBundleCanaryDeps {
	defaults := defaultQABundleCanaryDeps()
	if d.loadConfig == nil {
		d.loadConfig = defaults.loadConfig
	}
	if d.openDB == nil {
		d.openDB = defaults.openDB
	}
	if d.run == nil {
		d.run = defaults.run
	}
	return d
}

func qaBundleCanaryRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--qa-bundle-canary" || arg == "--qa-bundle-canary=true" {
			return true
		}
	}
	return false
}

func runQABundleCanaryCommand(
	ctx context.Context,
	args []string,
	out io.Writer,
	deps qaBundleCanaryDeps,
) error {
	fs := flag.NewFlagSet("qa-bundle-canary", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var enabled bool
	var confirmation string
	var timeoutSeconds int
	fs.BoolVar(&enabled, "qa-bundle-canary", false, "run the end-to-end QA Bundle canary")
	fs.StringVar(&confirmation, "confirm", "", "exact QA Bundle canary confirmation")
	fs.IntVar(&timeoutSeconds, "timeout-seconds", 600, "worker result timeout")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("qa bundle canary flags: %w", err)
	}
	if !enabled || fs.NArg() != 0 {
		return errors.New("qa bundle canary mode was not requested")
	}
	if strings.TrimSpace(confirmation) != qaBundleCanaryConfirmation {
		return errors.New("qa bundle canary confirmation mismatch")
	}
	if timeoutSeconds <= 0 || timeoutSeconds > 1800 {
		return errors.New("qa bundle canary timeout must be between 1 and 1800 seconds")
	}
	deps = deps.withDefaults()
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load qa bundle canary config: %w", err)
	}
	db, err := deps.openDB("postgres", cfg.Database.DSNWithTimezone(cfg.Timezone))
	if err != nil {
		return fmt.Errorf("open qa bundle canary database: %w", err)
	}
	defer func() { _ = db.Close() }()
	receipt, err := deps.run(ctx, cfg, db, time.Duration(timeoutSeconds)*time.Second)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(receipt); err != nil {
		return fmt.Errorf("encode qa bundle canary receipt: %w", err)
	}
	return nil
}
