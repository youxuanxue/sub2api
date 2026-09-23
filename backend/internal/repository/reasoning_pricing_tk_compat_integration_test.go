//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestReasoningPricingRollingCompatibility(t *testing.T) {
	for _, alreadyMigrated := range []bool{false, true} {
		name := "legacy"
		if alreadyMigrated {
			name = "upstream239"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := openMigrationIntegrationDatabase(t, "reasoning_compat_"+name)
			_, err := db.ExecContext(ctx, `
                CREATE TABLE channel_model_pricing (id bigserial PRIMARY KEY, max_reasoning_effort_multiplier numeric(10,4));
                CREATE TABLE channel_account_stats_model_pricing (id bigserial PRIMARY KEY);
                CREATE TABLE groups (id bigserial PRIMARY KEY, model_pricing jsonb);
                INSERT INTO channel_model_pricing (max_reasoning_effort_multiplier) VALUES (2.5), (NULL);
                INSERT INTO groups (model_pricing) VALUES
                    ('[{"models":["custom"],"max_reasoning_effort_multiplier":2.5}]'),
                    ('[]');`)
			require.NoError(t, err)
			original, readErr := dbmigrations.FS.ReadFile(supersededReasoningPricingMigration)
			require.NoError(t, readErr)
			if alreadyMigrated {
				_, err = db.ExecContext(ctx, string(original))
				require.NoError(t, err)
				// A post-239 administrator may already have cleared the new map.
				_, err = db.ExecContext(ctx, `
                    UPDATE channel_model_pricing SET max_reasoning_effort_multiplier=3, reasoning_effort_multipliers='{}' WHERE id=2;
                    UPDATE groups SET model_pricing='[{"max_reasoning_effort_multiplier":3,"reasoning_effort_multipliers":{}}]' WHERE id=2;`)
				require.NoError(t, err)
			} else {
				// An old reader must never observe the legacy field being removed,
				// even between migration transactions. Executing upstream 239 fails here.
				_, err = db.ExecContext(ctx, `
                    CREATE FUNCTION protect_legacy_price() RETURNS trigger LANGUAGE plpgsql AS $$
                    BEGIN
                        IF OLD.model_pricing->0 ? 'max_reasoning_effort_multiplier'
                           AND NOT (NEW.model_pricing->0 ? 'max_reasoning_effort_multiplier') THEN
                            RAISE EXCEPTION 'legacy price removed during upgrade';
                        END IF;
                        RETURN NEW;
                    END $$;
                    CREATE TRIGGER aa_protect_legacy_price BEFORE UPDATE ON groups
                    FOR EACH ROW EXECUTE FUNCTION protect_legacy_price();`)
				require.NoError(t, err)
				// Reproduce the unsafe transition before exercising the replacement.
				_, err = db.ExecContext(ctx, string(original))
				require.ErrorContains(t, err, "legacy price removed during upgrade")
			}
			migrationFS := migrationFilesFS(t, supersededReasoningPricingMigration, "tk_099_reasoning_pricing_rolling_compat.sql")
			require.NoError(t, applyMigrationsFS(ctx, db, migrationFS))
			if !alreadyMigrated {
				_, err = db.ExecContext(ctx, `DROP TRIGGER aa_protect_legacy_price ON groups`)
				require.NoError(t, err)
			}
			if alreadyMigrated {
				var cleared sql.NullFloat64
				require.NoError(t, db.QueryRow(`SELECT max_reasoning_effort_multiplier FROM channel_model_pricing WHERE id=2`).Scan(&cleared))
				require.False(t, cleared.Valid)
				var clearedGroup string
				require.NoError(t, db.QueryRow(`SELECT model_pricing::text FROM groups WHERE id=2`).Scan(&clearedGroup))
				require.JSONEq(t, `[{"reasoning_effort_multipliers":{}}]`, clearedGroup)
			}
			assertChannelReasoningCompat(t, db, `{"max":2.5}`, 2.5)
			assertGroupReasoningCompat(t, db, `[{"models":["custom"],"max_reasoning_effort_multiplier":2.5,"reasoning_effort_multipliers":{"max":2.5}}]`)

			// Inserts from either binary generation publish both representations.
			var inserted string
			require.NoError(t, db.QueryRow(`INSERT INTO channel_model_pricing (max_reasoning_effort_multiplier) VALUES (2.25) RETURNING reasoning_effort_multipliers::text`).Scan(&inserted))
			require.JSONEq(t, `{"max":2.25}`, inserted)
			var insertedMax float64
			require.NoError(t, db.QueryRow(`INSERT INTO channel_model_pricing (reasoning_effort_multipliers) VALUES ('{"max":4.25}') RETURNING max_reasoning_effort_multiplier`).Scan(&insertedMax))
			require.Equal(t, 4.25, insertedMax)
			require.NoError(t, db.QueryRow(`INSERT INTO groups (model_pricing) VALUES ('[{"max_reasoning_effort_multiplier":2.25}]') RETURNING model_pricing::text`).Scan(&inserted))
			require.JSONEq(t, `[{"max_reasoning_effort_multiplier":2.25,"reasoning_effort_multipliers":{"max":2.25}}]`, inserted)

			// New writers change and clear max; old readers see exactly that price.
			_, err = db.ExecContext(ctx, `UPDATE channel_model_pricing SET reasoning_effort_multipliers='{"max":4,"high":1.5}' WHERE id=1`)
			require.NoError(t, err)
			assertChannelReasoningCompat(t, db, `{"max":4,"high":1.5}`, 4.0)
			// The old column must represent the generic map without rounding/overflow.
			_, err = db.ExecContext(ctx, `UPDATE channel_model_pricing SET reasoning_effort_multipliers='{"max":1234567.123456,"high":1.5}' WHERE id=1`)
			require.NoError(t, err)
			assertChannelReasoningCompat(t, db, `{"max":1234567.123456,"high":1.5}`, 1234567.123456)
			// Old writers retain the other levels while editing or clearing max.
			_, err = db.ExecContext(ctx, `UPDATE channel_model_pricing SET max_reasoning_effort_multiplier=3 WHERE id=1`)
			require.NoError(t, err)
			assertChannelReasoningCompat(t, db, `{"max":3,"high":1.5}`, 3.0)
			_, err = db.ExecContext(ctx, `UPDATE channel_model_pricing SET max_reasoning_effort_multiplier=NULL WHERE id=1`)
			require.NoError(t, err)
			assertChannelReasoningCompat(t, db, `{"high":1.5}`, nil)
			_, err = db.ExecContext(ctx, `UPDATE channel_model_pricing SET reasoning_effort_multipliers='{}' WHERE id=1`)
			require.NoError(t, err)
			assertChannelReasoningCompat(t, db, `{}`, nil)

			for _, tc := range []struct{ input, want string }{
				{`[{"reasoning_effort_multipliers":{"max":4,"high":1.5}}]`, `[{"reasoning_effort_multipliers":{"max":4,"high":1.5},"max_reasoning_effort_multiplier":4}]`},
				{`[{"max_reasoning_effort_multiplier":2}]`, `[{"reasoning_effort_multipliers":{"max":2},"max_reasoning_effort_multiplier":2}]`},
				{`[{"max_reasoning_effort_multiplier":2,"reasoning_effort_multipliers":{}}]`, `[{"reasoning_effort_multipliers":{}}]`},
				{`[{},null,42]`, `[{},null,42]`},
				{`[]`, `[]`},
			} {
				_, err = db.ExecContext(ctx, `UPDATE groups SET model_pricing=$1 WHERE id=1`, tc.input)
				require.NoError(t, err)
				assertGroupReasoningCompat(t, db, tc.want)
			}
			// Reapplying the compatibility SQL must not revive a cleared price.
			compatSQL, readErr := dbmigrations.FS.ReadFile("tk_099_reasoning_pricing_rolling_compat.sql")
			require.NoError(t, readErr)
			_, err = db.ExecContext(ctx, string(compatSQL))
			require.NoError(t, err)
			require.NoError(t, applyMigrationsFS(ctx, db, migrationFS))
			assertChannelReasoningCompat(t, db, `{}`, nil)
			assertGroupReasoningCompat(t, db, `[]`)
		})
	}
}

func assertChannelReasoningCompat(t *testing.T, db *sql.DB, wantMap string, wantMax any) {
	t.Helper()
	var raw string
	var legacy sql.NullFloat64
	require.NoError(t, db.QueryRow(`SELECT reasoning_effort_multipliers::text, max_reasoning_effort_multiplier FROM channel_model_pricing WHERE id=1`).Scan(&raw, &legacy))
	require.JSONEq(t, wantMap, raw)
	if wantMax == nil {
		require.False(t, legacy.Valid)
	} else {
		require.True(t, legacy.Valid)
		require.Equal(t, wantMax, legacy.Float64)
	}
}

func assertGroupReasoningCompat(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	var raw string
	require.NoError(t, db.QueryRow(`SELECT model_pricing::text FROM groups WHERE id=1`).Scan(&raw))
	require.JSONEq(t, want, raw)
}
