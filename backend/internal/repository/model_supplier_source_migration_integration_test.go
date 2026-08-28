//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestModelSupplierSourceMigrationCreatesOnlyTheSingleSourceTable(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("tk_089_model_supplier_sources.sql")
	require.NoError(t, err)
	require.NoError(t, execMigrationStatements(ctx, tx, migrationSQL))

	models := `[{"client_model_id":"deepseek-v4-pro","upstream_model_id":"deepseek-v4-pro","purchase_ratio":0.5}]`
	var sourceID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO model_supplier_sources (
    supplier_name, channel_name, endpoint, encrypted_credential,
    credential_fingerprint, base_priority, models, notes
) VALUES ('佳杰', 'stbl-5', 'https://token.vstecscloud.com/v1', 'ciphertext', 'hmac:abc', 100, $1::jsonb, '')
RETURNING id
`, models).Scan(&sourceID))
	require.Positive(t, sourceID)

	columns, err := tableColumnNames(ctx, tx, "model_supplier_sources")
	require.NoError(t, err)
	require.Contains(t, columns, "encrypted_credential")
	require.Contains(t, columns, "credential_fingerprint")
	require.Contains(t, columns, "base_priority")
	require.Contains(t, columns, "models")
	require.NotContains(t, columns, "state")
	require.NotContains(t, columns, "revision")
	require.False(t, migrationTestTableExists(ctx, t, tx, "model_supplier_source_models"))
	require.False(t, migrationTestTableExists(ctx, t, tx, "model_supplier_source_audits"))

	_, err = tx.ExecContext(ctx, `
INSERT INTO model_supplier_sources (
    supplier_name, channel_name, endpoint, encrypted_credential,
    credential_fingerprint, base_priority, models, notes
) VALUES ('佳杰', 'stbl-5', 'https://token.vstecscloud.com/v1', 'rotated-ciphertext', 'hmac:abc', 100, $1::jsonb, '')
`, models)
	require.Error(t, err)
	var pqErr *pq.Error
	require.True(t, errors.As(err, &pqErr))
	require.Equal(t, pq.ErrorCode("23505"), pqErr.Code)
	require.Equal(t, "model_supplier_sources_identity_unique", pqErr.Constraint)
}

func migrationTestTableExists(ctx context.Context, t *testing.T, tx *sql.Tx, table string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = 'public' AND table_name = $1
)
`, table).Scan(&exists))
	return exists
}

func tableColumnNames(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT column_name
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = $1
ORDER BY ordinal_position
`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func execMigrationStatements(ctx context.Context, tx *sql.Tx, migrationSQL []byte) error {
	_, err := tx.ExecContext(ctx, string(migrationSQL))
	return err
}
