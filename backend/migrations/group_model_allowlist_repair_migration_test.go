//go:build integration

package migrations

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestGroupModelAllowlistRepairMigration(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("allowlist_repair"), postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"), postgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	const listing = `{"enabled":true,"models":["listing-only"]}`
	const policy = `{"enabled":true,"models":["admitted-model"]}`
	for _, schema := range []string{"public", "tenant"} {
		for _, state := range []struct {
			name       string
			columns    string
			values     string
			wantList   string
			wantPolicy string
		}{
			{"fresh", "", "", `{}`, `{}`},
			{"legacy_only", ", models_list_config jsonb", ", '" + listing + "'", listing, `{}`},
			{"policy_only", ", model_allowlist jsonb", ", '" + policy + "'", `{}`, policy},
			{"both", ", models_list_config jsonb, model_allowlist jsonb", ", '" + listing + "', '" + policy + "'", listing, policy},
			{"null_policy", ", models_list_config jsonb, model_allowlist jsonb", ", '" + listing + "', NULL", listing, `{}`},
		} {
			t.Run(schema+"/"+state.name, func(t *testing.T) {
				tx, err := db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback() }()
				exec := func(query string) {
					t.Helper()
					_, err := tx.ExecContext(ctx, query)
					require.NoError(t, err)
				}
				if schema == "tenant" {
					exec("CREATE SCHEMA tenant")
					// An unrelated public table must not affect search_path resolution.
					exec("CREATE TABLE public.groups (id bigint PRIMARY KEY)")
				}
				exec("SET LOCAL search_path TO " + schema)
				exec("CREATE TABLE groups (id bigint PRIMARY KEY" + state.columns + ")")
				exec("INSERT INTO groups VALUES (1" + state.values + ")")
				for range 2 {
					for _, name := range []string{"235_group_model_allowlist.sql", "236_group_model_allowlist_repair.sql"} {
						migration, err := FS.ReadFile(name)
						require.NoError(t, err)
						exec(string(migration))
					}
				}
				var gotList, gotPolicy string
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT models_list_config, model_allowlist FROM groups WHERE id=1").Scan(&gotList, &gotPolicy))
				require.JSONEq(t, state.wantList, gotList)
				require.JSONEq(t, state.wantPolicy, gotPolicy)
				exec(`UPDATE groups SET models_list_config = '{"models":["old-writer"]}' WHERE id=1`)
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT model_allowlist FROM groups WHERE id=1").Scan(&gotPolicy))
				require.JSONEq(t, state.wantPolicy, gotPolicy)
				exec("INSERT INTO groups (id) VALUES (2)")
				require.NoError(t, tx.QueryRowContext(ctx, "SELECT model_allowlist FROM groups WHERE id=2").Scan(&gotPolicy))
				require.JSONEq(t, `{}`, gotPolicy)
				_, err = tx.ExecContext(ctx, "UPDATE groups SET model_allowlist=NULL WHERE id=2")
				require.ErrorContains(t, err, "not-null constraint")
			})
		}
	}
}
