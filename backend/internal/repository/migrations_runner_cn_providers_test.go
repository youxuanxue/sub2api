package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShouldRecordMigrationWithoutExecution_SkipsUpstreamCNProviderConstraint(t *testing.T) {
	ctx := context.Background()

	skip, err := shouldRecordMigrationWithoutExecution(ctx, nil, userPlatformQuotasCNProvidersMigration)
	require.NoError(t, err)
	require.True(t, skip,
		"upstream 224's CHECK omits live TK platforms newapi/kiro; executing it blocks startup before tk_087")

	skip, err = shouldRecordMigrationWithoutExecution(ctx, nil, "157_user_platform_quotas_add_grok.sql")
	require.NoError(t, err)
	require.False(t, skip, "unrelated quota migrations must still execute")
}
