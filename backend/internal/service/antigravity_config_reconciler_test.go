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
	r := NewAntigravityConfigReconciler(acc, nil, nil, nil)
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
	require.NotContains(t, mm, "tab_flash_lite_preview",
		"unpriced Antigravity models must not be written into account mappings")
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
	r := NewAntigravityConfigReconciler(acc, nil, nil, nil)
	r.runOnce(context.Background())

	require.Empty(t, acc.bulkCalls, "already-gemini-only account must not be rewritten")
}

func TestAntigravityReconciler_UnpricedTabModel_ReconciledOut(t *testing.T) {
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformAntigravity: {
				{
					ID:       9,
					Platform: PlatformAntigravity,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"gemini-3.5-flash-low":   "gemini-3.5-flash-low",
							"tab_flash_lite_preview": "tab_flash_lite_preview",
						},
					},
				},
			},
		},
	}
	r := NewAntigravityConfigReconciler(acc, nil, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1, "persisted $0-risk tab model must be reconciled out")
	mm, ok := acc.bulkCalls[0].updates.Credentials["model_mapping"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, mm, "tab_flash_lite_preview")
	require.Contains(t, mm, "gemini-3.5-flash-low")
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
	r := NewAntigravityConfigReconciler(acc, nil, nil, nil)
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
	r := NewAntigravityConfigReconciler(acc, nil, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1, "custom map with claude-opus must be reconciled")
	require.Equal(t, []int64{9}, acc.bulkCalls[0].ids)
	mm := acc.bulkCalls[0].updates.Credentials["model_mapping"].(map[string]any)
	require.NotContains(t, mm, "claude-opus-4-8")
}

func TestAntigravityReconciler_StructuralDeadAlias_StillReconciled(t *testing.T) {
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformAntigravity: {
				{
					ID:       10,
					Platform: PlatformAntigravity,
					Credentials: map[string]any{
						"model_mapping": map[string]any{
							"gemini-3-pro-preview": "gemini-pro-agent",
							"gemini-pro-agent":     "gemini-pro-agent",
						},
					},
				},
			},
		},
	}
	r := NewAntigravityConfigReconciler(acc, nil, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1, "custom map with structural-dead alias must be reconciled")
	require.Equal(t, []int64{10}, acc.bulkCalls[0].ids)
	mm := acc.bulkCalls[0].updates.Credentials["model_mapping"].(map[string]any)
	require.NotContains(t, mm, "gemini-3-pro-preview")
	require.Contains(t, mm, "gemini-pro-agent")
}

// Nil store / nil reconciler must be safe (mirrors the wire minimal-deps smoke).
func TestAntigravityReconciler_NilSafe(t *testing.T) {
	var nilRec *AntigravityConfigReconciler
	require.NotPanics(t, func() { nilRec.runOnce(context.Background()); nilRec.Start(); nilRec.Stop() })

	rec := NewAntigravityConfigReconciler(nil, nil, nil, nil)
	require.NotPanics(t, func() { rec.runOnce(context.Background()); rec.Start(); rec.Stop() })
}

// --- group scope reconciliation ---

type reconcilerGroupStub struct {
	byPlatform  map[string][]Group
	updateCalls []Group
	listErr     error
}

func (s *reconcilerGroupStub) ListActiveByPlatform(_ context.Context, platform string) ([]Group, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.byPlatform[platform], nil
}

func (s *reconcilerGroupStub) Update(_ context.Context, g *Group) error {
	s.updateCalls = append(s.updateCalls, *g)
	return nil
}

func TestAntigravityGroupScopesNeedGeminiOnly(t *testing.T) {
	cases := []struct {
		scopes []string
		drift  bool
	}{
		{nil, true},        // empty = unrestricted → advertises claude
		{[]string{}, true}, // empty
		{[]string{"claude", "gemini_text", "gemini_image"}, true}, // default (includes claude)
		{[]string{"gemini_text"}, true},                           // missing image
		{[]string{"gemini_text", "gemini_text"}, true},            // duplicate, missing image
		{[]string{"gemini_text", "gemini_image"}, false},          // canonical
		{[]string{"gemini_image", "gemini_text"}, false},          // canonical, order-independent
	}
	for _, c := range cases {
		if got := antigravityGroupScopesNeedGeminiOnly(c.scopes); got != c.drift {
			t.Fatalf("antigravityGroupScopesNeedGeminiOnly(%v)=%v want %v", c.scopes, got, c.drift)
		}
	}
}

func TestAntigravityReconciler_GroupScopes_HealsDriftedToGeminiOnly(t *testing.T) {
	grp := &reconcilerGroupStub{
		byPlatform: map[string][]Group{
			PlatformAntigravity: {
				{ID: 1, Platform: PlatformAntigravity, SupportedModelScopes: []string{"claude", "gemini_text", "gemini_image"}}, // drift
				{ID: 2, Platform: PlatformAntigravity, SupportedModelScopes: []string{"gemini_text", "gemini_image"}},           // aligned → skip
				{ID: 3, Platform: PlatformAntigravity, SupportedModelScopes: nil},                                               // empty → drift
			},
		},
	}
	r := NewAntigravityConfigReconciler(nil, grp, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, grp.updateCalls, 2, "only the two drifted groups should be updated")
	ids := []int64{grp.updateCalls[0].ID, grp.updateCalls[1].ID}
	require.ElementsMatch(t, []int64{1, 3}, ids)
	for _, g := range grp.updateCalls {
		require.Equal(t, domain.GeminiOnlyAntigravityModelScopes, g.SupportedModelScopes,
			"healed group %d must carry canonical gemini-only scopes", g.ID)
	}
}

func TestAntigravityReconciler_GroupScopes_NilStoreSafe(t *testing.T) {
	// accounts present, groups nil → group reconcile no-ops, no panic.
	acc := &reconcilerAccountStub{byPlatform: map[string][]Account{}}
	r := NewAntigravityConfigReconciler(acc, nil, nil, nil)
	require.NotPanics(t, func() { r.runOnce(context.Background()) })
}
