package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnableGeminiImageGenerationGroupsMigration(t *testing.T) {
	content, err := FS.ReadFile("246_enable_gemini_image_generation_groups.sql")
	require.NoError(t, err)

	sql := strings.ToLower(string(content))
	require.NotContains(t, sql, "update groups")
	require.NotContains(t, sql, "allow_image_generation = true")
	require.Contains(t, sql, "select 1")
}
