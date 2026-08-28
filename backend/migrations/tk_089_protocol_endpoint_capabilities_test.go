package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTK089DefinesEndpointScopedCapabilityOwnership(t *testing.T) {
	content, err := FS.ReadFile("tk_089_protocol_endpoint_capabilities.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, contract := range []string{
		"CREATE TABLE IF NOT EXISTS protocol_endpoint_capabilities",
		"capability_key VARCHAR(64) NOT NULL UNIQUE",
		"identity JSONB NOT NULL",
		"supported_protocols JSONB NOT NULL DEFAULT '[]'::jsonb",
		"probe_evidence JSONB NOT NULL DEFAULT '{}'::jsonb",
		"revision BIGINT NOT NULL DEFAULT 1",
		"probe_generation BIGINT NOT NULL DEFAULT 0",
		"identity_conflict BOOLEAN NOT NULL DEFAULT FALSE",
		"ADD COLUMN IF NOT EXISTS protocol_endpoint_capability_id BIGINT",
		"ON DELETE RESTRICT",
	} {
		require.Contains(t, sql, contract)
	}
	require.Contains(t, sql, "jsonb_typeof(supported_protocols) = 'array'")
	require.Contains(t, sql, "prevent_protocol_endpoint_identity_mutation")
}
