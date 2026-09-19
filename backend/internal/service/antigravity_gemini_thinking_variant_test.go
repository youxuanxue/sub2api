//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveGeminiThinkingVariant(t *testing.T) {
	mapping := map[string]any{
		"gemini-3.8-flash-low":    "gemini-3.8-flash-low",
		"gemini-3.8-flash-medium": "gemini-3.8-flash-medium",
		"gemini-3.8-flash-high":   "gemini-3.8-flash-high",
		"gemini-3.8-flash-tiered": "gemini-3.8-flash-tiered",
	}
	account := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{
		"model_mapping": mapping,
	}}

	tests := []struct {
		name  string
		body  string
		want  string
		match bool
	}{
		{name: "low budget", body: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":1000}}}`, want: "gemini-3.8-flash-low", match: true},
		{name: "medium budget", body: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":4000}}}`, want: "gemini-3.8-flash-medium", match: true},
		{name: "dynamic budget", body: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`, want: "gemini-3.8-flash-high", match: true},
		{name: "level wins", body: `{"generationConfig":{"thinkingConfig":{"thinkingBudget":1000,"thinkingLevel":"high"}}}`, want: "gemini-3.8-flash-high", match: true},
		{name: "no thinking config defaults high", body: `{"contents":[]}`, want: "gemini-3.8-flash-high", match: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, matched := resolveGeminiThinkingVariant(account, "models/gemini-3.8-flash", []byte(tt.body))
			require.Equal(t, tt.match, matched)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestResolveGeminiThinkingVariantPreservesExplicitBareMapping(t *testing.T) {
	account := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{
		"model_mapping": map[string]any{
			"gemini-3.8-flash":      "gemini-3.8-flash",
			"gemini-3.8-flash-high": "gemini-3.8-flash-high",
		},
	}}

	got, matched := resolveGeminiThinkingVariant(account, "gemini-3.8-flash", []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`))
	require.False(t, matched)
	require.Empty(t, got)
}
