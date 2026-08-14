//go:build integration

package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/observability/qa/lifecycle"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestUS045_QABoundaryPooledConnectionsApplyLockTimeoutAndRetry(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	container, err := postgres.Run(
		ctx,
		"postgres:18.1-alpine3.23",
		postgres.WithDatabase("qa_boundary_lock_retry"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("start postgres: %v", err)
	}
	defer func() { _ = container.Terminate(ctx) }()

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)
	databaseConfig := config.DatabaseConfig{
		Host: host, Port: port.Int(), User: "postgres", Password: "postgres",
		DBName: "qa_boundary_lock_retry", SSLMode: "disable",
	}
	adminDB, err := sql.Open("postgres", databaseConfig.DSNWithTimezone("UTC"))
	require.NoError(t, err)
	defer func() { _ = adminDB.Close() }()
	require.NoError(t, adminDB.PingContext(ctx))
	_, err = adminDB.ExecContext(ctx, `
CREATE TABLE qa_records (
    id bigint NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at)`)
	require.NoError(t, err)

	holder, err := adminDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback() }()
	_, err = holder.ExecContext(ctx, `LOCK TABLE qa_records IN ACCESS SHARE MODE`)
	require.NoError(t, err)

	boundaryDB, err := openQABoundaryDB(qaBoundaryDeps{
		loadConfig: func() (*config.Config, error) {
			return &config.Config{Timezone: "UTC", Database: databaseConfig}, nil
		},
	})
	require.NoError(t, err)
	defer func() { _ = boundaryDB.Close() }()

	commitResult := make(chan error, 1)
	time.AfterFunc(225*time.Millisecond, func() {
		commitResult <- holder.Commit()
	})
	started := time.Now()
	result, err := lifecycle.RunProvision(
		ctx,
		boundaryDB,
		lifecycle.Options{HoursAhead: 1},
		nil,
	)
	require.NoError(t, err)
	require.NoError(t, <-commitResult)
	require.Equal(t, 2, result.Attempts)
	require.Equal(t, 1, result.LockRetries)
	require.Equal(t, 1, result.RangesCovered)
	require.GreaterOrEqual(t, time.Since(started), 300*time.Millisecond)
	require.Less(t, time.Since(started), 2*time.Second)
}
