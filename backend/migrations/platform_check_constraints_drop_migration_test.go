package migrations

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDropPlatformCheckConstraintsMigration(t *testing.T) {
	content, err := FS.ReadFile("242_drop_platform_check_constraints.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		"ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;")
	require.Contains(t, sql,
		"ALTER TABLE composite_model_routes DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;")
	require.NotContains(t, sql, "ADD CONSTRAINT")
	require.NotContains(t, sql, "DROP CONSTRAINT IF EXISTS channel_monitors_provider_check")
	require.NotContains(t, sql, "DROP CONSTRAINT IF EXISTS channel_monitor_request_templates_provider_check")
}

// Platform membership is validated against the application catalog after 242.
// Historical TK migrations that re-ADD the check stay immutable; a later
// migration must DROP the same constraint so the applied schema does not keep it.
func TestLaterMigrationsDoNotRestorePlatformCheckConstraints(t *testing.T) {
	const droppedAt = "242_drop_platform_check_constraints.sql"
	entries, err := fs.ReadDir(FS, ".")
	require.NoError(t, err)
	comments := regexp.MustCompile(`(?ms)/\*.*?\*/|--[^\n]*`)
	addConstraint := regexp.MustCompile(`(?i)\bADD\s+CONSTRAINT\s+"?(user_platform_quotas_platform_check|composite_model_routes_target_platform_check)\b`)
	dropConstraint := regexp.MustCompile(`(?i)\bDROP\s+CONSTRAINT\s+IF\s+EXISTS\s+"?(user_platform_quotas_platform_check|composite_model_routes_target_platform_check)\b`)
	type laterSQL struct {
		name string
		sql  []byte
	}
	var later []laterSQL
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") || entry.Name() <= droppedAt {
			continue
		}
		content, err := FS.ReadFile(entry.Name())
		require.NoError(t, err)
		later = append(later, laterSQL{name: entry.Name(), sql: comments.ReplaceAll(content, nil)})
	}
	for i, file := range later {
		if addConstraint.Find(file.sql) == nil {
			continue
		}
		droppedLater := false
		for _, follow := range later[i+1:] {
			if dropConstraint.Find(follow.sql) != nil {
				droppedLater = true
				break
			}
		}
		require.Truef(t, droppedLater, "%s re-adds a platform CHECK removed in 242; a later migration must drop it", file.name)
	}
}
