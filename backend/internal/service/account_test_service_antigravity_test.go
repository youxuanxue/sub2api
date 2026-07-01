//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestMapAntigravityModel_GeminiOnlyAccountRejectsClaudeProbe(t *testing.T) {
	geminiOnly := make(map[string]any, len(domain.GeminiOnlyAntigravityModelMapping))
	for k, v := range domain.GeminiOnlyAntigravityModelMapping {
		geminiOnly[k] = v
	}
	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": geminiOnly,
		},
	}

	require.NotEmpty(t, MapAntigravityModel(account, AntigravityDefaultTestModelID))
	require.Empty(t, MapAntigravityModel(account, "claude-sonnet-4-5"))
}

func TestAntigravityDefaultTestModelID_IsGeminiWire(t *testing.T) {
	require.True(t, len(AntigravityDefaultTestModelID) > len("gemini-"))
	require.Contains(t, AntigravityDefaultTestModelID, "gemini-")
}
