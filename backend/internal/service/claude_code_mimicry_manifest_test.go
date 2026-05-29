package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseClaudeCodeHTTPMimicryManifest_Valid(t *testing.T) {
	raw := `{"schema_version":1,"cc_version":"2.1.156","sonnet_opus":["oauth-2025-04-20","claude-code-20250219"],"haiku":["oauth-2025-04-20"]}`
	m := ParseClaudeCodeHTTPMimicryManifest(raw)
	require.NotNil(t, m)
	require.Equal(t, "2.1.156", m.CCVersion)
	require.Equal(t, []string{"oauth-2025-04-20", "claude-code-20250219"}, m.SonnetOpus)
	require.Equal(t, []string{"oauth-2025-04-20"}, m.Haiku)
}

func TestParseClaudeCodeHTTPMimicryManifest_RejectsInvalid(t *testing.T) {
	cases := []string{
		"",
		"not-json",
		`{"schema_version":0}`,
		`{"schema_version":1,"cc_version":"bad","sonnet_opus":["x"],"haiku":["oauth-2025-04-20"]}`,
		`{"schema_version":1,"cc_version":"2.1.156","sonnet_opus":[],"haiku":["oauth-2025-04-20"]}`,
	}
	for _, raw := range cases {
		require.Nil(t, ParseClaudeCodeHTTPMimicryManifest(raw), "raw=%q", raw)
	}
}
