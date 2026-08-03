package repository

import (
	"context"
	"database/sql"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/telemetryarchive"
)

func ProvideTelemetryArchive(cfg *config.Config) *telemetryarchive.Shadow {
	archive := cfg.TelemetryArchive
	return telemetryarchive.NewS3(
		context.Background(),
		archive.Region,
		telemetryarchive.ConfigFromValues(
			archive.Enabled,
			archive.Bucket,
			archive.Prefix,
			archive.QueueSize,
			archive.BatchSize,
			archive.FlushIntervalSeconds,
			archive.PutTimeoutSeconds,
		),
	)
}

func ProvideUsageLogRepository(
	client *dbent.Client,
	sqlDB *sql.DB,
	archive *telemetryarchive.Shadow,
) service.UsageLogRepository {
	repo := newUsageLogRepositoryWithSQL(client, sqlDB)
	repo.telemetry = archive
	return repo
}

func ProvideOpsRepository(db *sql.DB, archive *telemetryarchive.Shadow) service.OpsRepository {
	return &opsRepository{db: db, telemetry: archive}
}
