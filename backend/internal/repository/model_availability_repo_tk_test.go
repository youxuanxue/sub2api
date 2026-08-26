package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/modelavailability"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func newModelAvailabilityRepoSQLite(t *testing.T) (*modelAvailabilityRepository, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	return &modelAvailabilityRepository{client: client, muCells: make(map[string]*sync.Mutex)}, client
}

func TestModelAvailabilityRepositoryGetBatchFiltersPlatformAndMissingModels(t *testing.T) {
	repo, client := newModelAvailabilityRepoSQLite(t)
	ctx := context.Background()

	_, err := client.ModelAvailability.Create().
		SetPlatform(modelavailability.PlatformOpenai).
		SetModelID("model-a").
		SetStatus(modelavailability.StatusOk).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.ModelAvailability.Create().
		SetPlatform(modelavailability.PlatformAnthropic).
		SetModelID("model-a").
		SetStatus(modelavailability.StatusUnreachable).
		Save(ctx)
	require.NoError(t, err)

	got, err := repo.GetBatch(ctx, service.PlatformOpenAI, []string{"model-a", "model-missing"})

	require.NoError(t, err)
	require.Equal(t, map[string]service.AvailabilityState{
		"model-a": {Platform: service.PlatformOpenAI, ModelID: "model-a", Status: service.AvailabilityStatusOK},
	}, got)
}
