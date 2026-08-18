package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectModelFamily(t *testing.T) {
	tests := map[string]ModelFamily{
		"claude-sonnet-4-6":         "claude",
		"anthropic.claude-opus-4-6": "claude",
		"gpt-5.4":                   "gpt",
		"gemini-3.1-pro":            "gemini",
		"grok-4":                    "grok",
		"deepseek-v3":               "deepseek",
		"qwen3-coder":               "qwen",
		"glm-4.7":                   "glm",
		"minimax-m2":                "minimax",
		"vendor-new-model":          "",
		"  vendor-new-model  ":      "",
	}

	for model, want := range tests {
		t.Run(model, func(t *testing.T) {
			require.Equal(t, want, DetectModelFamily(model))
		})
	}
}

func TestModelFamilyArtifactIsStableAndSelfVerifying(t *testing.T) {
	artifact := ExportModelFamilyRules()
	require.Equal(t, 1, artifact.SchemaVersion)
	require.Equal(t, []string{"amazon.", "anthropic.", "google.", "openai.", "xai."}, artifact.ProviderQualifiers)
	require.Equal(t, []ModelFamilyRule{
		{Family: "claude", Prefixes: []string{"claude-"}},
		{Family: "gpt", Prefixes: []string{"gpt-", "o1", "o3", "o4"}},
		{Family: "gemini", Prefixes: []string{"gemini-"}},
		{Family: "grok", Prefixes: []string{"grok-"}},
		{Family: "deepseek", Prefixes: []string{"deepseek-"}},
		{Family: "qwen", Prefixes: []string{"qwen"}},
		{Family: "glm", Prefixes: []string{"glm-"}},
		{Family: "minimax", Prefixes: []string{"minimax-"}},
	}, artifact.Rules)
	require.NotEmpty(t, artifact.Checksum)

	encoded, err := json.Marshal(artifact)
	require.NoError(t, err)
	require.True(t, VerifyModelFamilyRulesArtifact(encoded))

	encoded[len(encoded)-2] ^= 1
	require.False(t, VerifyModelFamilyRulesArtifact(encoded))
}
