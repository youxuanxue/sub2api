package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The four opt-out kill-switch keys this migration seeds. Kept literal on
// purpose: the migration is SQL text, so this test is what pins the key spelling
// against a rename in service/domain_constants.go.
var tk089SeededKeys = []string{
	"gateway.anthropic_saturated_stub_deprioritize.enabled",
	"gateway.openai_saturated_stub_deprioritize.enabled",
	"gateway.sticky_routing.enabled",
	"gateway.sticky_slot_full_escape.enabled",
}

func tk089SQL(t *testing.T) string {
	t.Helper()
	content, err := FS.ReadFile("tk_089_seed_optout_killswitch_settings.sql")
	require.NoError(t, err)
	return strings.Join(strings.Fields(string(content)), " ")
}

func TestTK089SeedsAllFourKillSwitchKeys(t *testing.T) {
	sql := tk089SQL(t)

	require.Contains(t, sql, "INSERT INTO settings")
	for _, key := range tk089SeededKeys {
		require.Containsf(t, sql, "'"+key+"'", "migration must seed %s", key)
	}
}

// The seeded value must be the effective default these deployments already had
// via fail-open, so applying the migration changes no behavior.
func TestTK089SeedsDefaultOnNotOff(t *testing.T) {
	sql := tk089SQL(t)

	require.Contains(t, sql, "'true'")
	require.NotContains(t, sql, "'false'",
		"seeding 'false' would silently disable a live gateway feature")
}

// ON CONFLICT DO NOTHING is what makes this both re-runnable and safe for an
// operator who has already turned one of these off by hand. DO UPDATE would
// stomp that choice on every deploy.
func TestTK089IsIdempotentAndPreservesOperatorOverrides(t *testing.T) {
	sql := tk089SQL(t)

	require.Contains(t, sql, "ON CONFLICT (key) DO NOTHING")
	require.NotContains(t, sql, "DO UPDATE",
		"DO UPDATE would overwrite an operator's explicit 'false' on redeploy")
}

// Seeding must never remove or rewrite existing rows.
func TestTK089IsNonDestructive(t *testing.T) {
	sql := tk089SQL(t)

	require.NotContains(t, sql, "DELETE FROM settings")
	require.NotContains(t, sql, "TRUNCATE")
	require.NotContains(t, sql, "DROP ")
	require.NotContains(t, sql, "UPDATE settings SET")
}

// Bounded lock/statement timeouts keep a stuck migration from blocking a deploy.
func TestTK089BoundsLockAndStatementTime(t *testing.T) {
	sql := tk089SQL(t)

	require.Contains(t, sql, "SET LOCAL lock_timeout")
	require.Contains(t, sql, "SET LOCAL statement_timeout")
}
