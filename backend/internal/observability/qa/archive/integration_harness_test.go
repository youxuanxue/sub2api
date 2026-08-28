//go:build integration

package archive

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const archiveIntegrationPostgresImage = "postgres:18.1-alpine3.23"

var (
	archiveIntegrationPostgresDSN      string
	archiveIntegrationPostgresStartErr error
	archiveIntegrationDatabaseName     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

func TestMain(m *testing.M) {
	startedAt := time.Now()
	ctx := context.Background()
	container, err := postgres.Run(
		ctx,
		archiveIntegrationPostgresImage,
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		archiveIntegrationPostgresStartErr = fmt.Errorf("start shared postgres: %w", err)
		log.Printf("archive-integration: STAGE postgres-unavailable (%.3fs): %v", time.Since(startedAt).Seconds(), err)
		os.Exit(m.Run())
	}

	archiveIntegrationPostgresDSN, err = container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		archiveIntegrationPostgresStartErr = fmt.Errorf("shared postgres connection string: %w", err)
		_ = container.Terminate(ctx)
		log.Printf("archive-integration: STAGE postgres-unavailable (%.3fs): %v", time.Since(startedAt).Seconds(), err)
		os.Exit(m.Run())
	}
	log.Printf("archive-integration: STAGE postgres-ready (%.3fs)", time.Since(startedAt).Seconds())

	testsStartedAt := time.Now()
	code := m.Run()
	log.Printf("archive-integration: STAGE tests-finished (%.3fs)", time.Since(testsStartedAt).Seconds())

	cleanupStartedAt := time.Now()
	if err := container.Terminate(ctx); err != nil {
		log.Printf("archive-integration: shared postgres cleanup failed: %v", err)
	}
	log.Printf("archive-integration: STAGE postgres-cleanup (%.3fs)", time.Since(cleanupStartedAt).Seconds())
	os.Exit(code)
}

func openArchiveIntegrationDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	if archiveIntegrationPostgresStartErr != nil {
		t.Skipf("shared postgres unavailable: %v", archiveIntegrationPostgresStartErr)
	}
	if archiveIntegrationPostgresDSN == "" {
		t.Fatal("shared postgres DSN is empty")
	}
	if !archiveIntegrationDatabaseName.MatchString(name) {
		t.Fatalf("invalid archive integration database name %q", name)
	}

	ctx := context.Background()
	admin, err := sql.Open("postgres", archiveIntegrationPostgresDSN)
	require.NoError(t, err)
	quotedName := pq.QuoteIdentifier(name)
	_, err = admin.ExecContext(ctx, "DROP DATABASE IF EXISTS "+quotedName+" WITH (FORCE)")
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+quotedName)
	require.NoError(t, err)
	require.NoError(t, admin.Close())

	dsn, err := archiveIntegrationDatabaseDSN(name)
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	require.NoError(t, db.PingContext(ctx))
	t.Cleanup(func() {
		_ = db.Close()
		cleanup, cleanupErr := sql.Open("postgres", archiveIntegrationPostgresDSN)
		require.NoError(t, cleanupErr)
		defer func() { _ = cleanup.Close() }()
		_, cleanupErr = cleanup.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+quotedName+" WITH (FORCE)")
		require.NoError(t, cleanupErr)
	})
	return db
}

func archiveIntegrationDatabaseDSN(name string) (string, error) {
	parsed, err := url.Parse(archiveIntegrationPostgresDSN)
	if err != nil {
		return "", fmt.Errorf("parse shared postgres DSN: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("shared postgres DSN is not a URL")
	}
	parsed.Path = "/" + name
	return parsed.String(), nil
}

func TestArchiveIntegrationHarnessProvidesIsolatedDatabases(t *testing.T) {
	ctx := context.Background()
	first := openArchiveIntegrationDB(t, "qa_archive_harness_first")
	second := openArchiveIntegrationDB(t, "qa_archive_harness_second")

	_, err := first.ExecContext(ctx, `CREATE TABLE isolation_probe (value text NOT NULL)`)
	require.NoError(t, err)
	_, err = first.ExecContext(ctx, `INSERT INTO isolation_probe (value) VALUES ('first')`)
	require.NoError(t, err)

	var visibleInFirst int
	require.NoError(t, first.QueryRowContext(ctx, `SELECT count(*) FROM isolation_probe`).Scan(&visibleInFirst))
	require.Equal(t, 1, visibleInFirst)

	var visibleInSecond bool
	require.NoError(t, second.QueryRowContext(ctx, `SELECT to_regclass('public.isolation_probe') IS NOT NULL`).Scan(&visibleInSecond))
	require.False(t, visibleInSecond, "shared PostgreSQL must still isolate each archive test database")
}
