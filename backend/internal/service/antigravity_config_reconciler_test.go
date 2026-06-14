//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"

	"github.com/stretchr/testify/require"
)

// An antigravity account with an empty model_mapping falls back to
// DefaultAntigravityModelMapping (which includes claude + gpt-oss), so it must be
// reconciled to gemini-only.
func TestAntigravityReconciler_EmptyMapAccount_ReconciledToGeminiOnly(t *testing.T) {
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformAntigravity: {
				{ID: 7, Platform: PlatformAntigravity, Credentials: nil},
			},
		},
	}
	r := NewAntigravityConfigReconciler(acc, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1, "expected one BulkUpdate for the claude-serving account")
	require.Equal(t, []int64{7}, acc.bulkCalls[0].ids)

	mm, ok := acc.bulkCalls[0].updates.Credentials["model_mapping"].(map[string]any)
	require.True(t, ok, "BulkUpdate must set credentials.model_mapping")
	for k := range mm {
		require.Falsef(t, strings.HasPrefix(k, "claude-"), "claude key leaked: %q", k)
		require.Falsef(t, strings.HasPrefix(k, "gpt-oss-"), "gpt-oss key leaked: %q", k)
	}
	require.Contains(t, mm, "gemini-3.5-flash-low")
	require.Contains(t, mm, "gemini-3.5-flash-extra-low")
	require.Contains(t, mm, "tab_flash_lite_preview")
	require.Len(t, mm, len(domain.GeminiOnlyAntigravityModelMapping))
}

// An account already carrying a gemini-only custom model_mapping cannot serve
// claude/gpt-oss → skip-if-aligned (no write, no thrash).
func TestAntigravityReconciler_AlreadyGeminiOnly_Skip(t *testing.T) {
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformAntigravity: {
				{
					ID:       8,
					Platform: PlatformAntigravity,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"gemini-3.5-flash-low": "gemini-3.5-flash-low",
							"gemini-pro-agent":     "gemini-pro-agent",
						},
					},
				},
			},
		},
	}
	r := NewAntigravityConfigReconciler(acc, nil, nil)
	r.runOnce(context.Background())

	require.Empty(t, acc.bulkCalls, "already-gemini-only account must not be rewritten")
}

// Mixed list: only the claude-serving account is reconciled; the gemini-only one
// is left alone (single BulkUpdate carrying only the drifted id).
func TestAntigravityReconciler_OnlyDriftedAccountsRewritten(t *testing.T) {
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformAntigravity: {
				{ID: 1, Platform: PlatformAntigravity, Credentials: nil}, // drifts (default → claude)
				{
					ID:       2,
					Platform: PlatformAntigravity,
					Credentials: map[string]any{
						"model_mapping": map[string]any{"gemini-3.5-flash-low": "gemini-3.5-flash-low"},
					},
				}, // already gemini-only
			},
		},
	}
	r := NewAntigravityConfigReconciler(acc, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1)
	require.Equal(t, []int64{1}, acc.bulkCalls[0].ids)
}

// R-001 regression: a custom mapping that serves a claude id OTHER than the probe
// id (claude-opus-4-8, no claude-sonnet-4-6, no gpt-oss, no wildcard) must still be
// detected as drifted and reconciled — the key scan catches any claude-* key, not
// just the probe id. This aligns the reconciler with the post-rollout check, which
// flags any claude-*/gpt-oss-* key.
func TestAntigravityReconciler_NonProbeClaudeId_StillReconciled(t *testing.T) {
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformAntigravity: {
				{
					ID:       9,
					Platform: PlatformAntigravity,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"claude-opus-4-8":      "claude-opus-4-8",
							"gemini-3.5-flash-low": "gemini-3.5-flash-low",
						},
					},
				},
			},
		},
	}
	r := NewAntigravityConfigReconciler(acc, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1, "custom map with claude-opus must be reconciled")
	require.Equal(t, []int64{9}, acc.bulkCalls[0].ids)
	mm := acc.bulkCalls[0].updates.Credentials["model_mapping"].(map[string]any)
	require.NotContains(t, mm, "claude-opus-4-8")
}

// Nil store / nil reconciler must be safe (mirrors the wire minimal-deps smoke).
func TestAntigravityReconciler_NilSafe(t *testing.T) {
	var nilRec *AntigravityConfigReconciler
	require.NotPanics(t, func() { nilRec.runOnce(context.Background()); nilRec.Start(); nilRec.Stop() })

	rec := NewAntigravityConfigReconciler(nil, nil, nil)
	require.NotPanics(t, func() { rec.runOnce(context.Background()); rec.Start(); rec.Stop() })
}
