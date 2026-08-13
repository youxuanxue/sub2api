//go:build integration

package repository

import (
	"context"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigrationTK081CutsOverKiroTLSProfileAtomically(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	migrationSQL, err := dbmigrations.FS.ReadFile("tk_081_kiro_cli_tls_profile.sql")
	require.NoError(t, err)

	var oldID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO tls_fingerprint_profiles (name)
VALUES ('tk_canonical_kiro_ide')
ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
RETURNING id
`).Scan(&oldID))

	var boundID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-tk081-bound', 'kiro', 'oauth', jsonb_build_object('tls_fingerprint_profile_id', $1::bigint))
RETURNING id
`, oldID).Scan(&boundID))

	var operatorProfileID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO tls_fingerprint_profiles (name)
VALUES ('migration-tk081-operator-profile')
RETURNING id
`).Scan(&operatorProfileID))
	var operatorBoundID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
INSERT INTO accounts (name, platform, type, extra)
VALUES ('migration-tk081-operator-bound', 'kiro', 'oauth', jsonb_build_object('tls_fingerprint_profile_id', $1::bigint))
RETURNING id
`, operatorProfileID).Scan(&operatorBoundID))

	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var newID int64
	var shuffled bool
	var ciphers string
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT id, shuffle_extensions, cipher_suites::text
FROM tls_fingerprint_profiles
WHERE name = 'tk_canonical_kiro_cli'
`).Scan(&newID, &shuffled, &ciphers))
	require.True(t, shuffled)
	require.JSONEq(t, `[4866,4865,4867,49196,49195,52393,49200,49199,52392,255]`, ciphers)

	var reboundProfileID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT (extra->>'tls_fingerprint_profile_id')::bigint
FROM accounts WHERE id = $1
`, boundID).Scan(&reboundProfileID))
	require.Equal(t, newID, reboundProfileID)

	var preservedProfileID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT (extra->>'tls_fingerprint_profile_id')::bigint
FROM accounts WHERE id = $1
`, operatorBoundID).Scan(&preservedProfileID))
	require.Equal(t, operatorProfileID, preservedProfileID)

	var oldRows int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM tls_fingerprint_profiles WHERE name = 'tk_canonical_kiro_ide'
`).Scan(&oldRows))
	require.Zero(t, oldRows)

	var outboxEvents int
	require.NoError(t, tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM scheduler_outbox
WHERE event_type = 'account_changed' AND account_id = $1
`, boundID).Scan(&outboxEvents))
	require.Equal(t, 1, outboxEvents)
}
