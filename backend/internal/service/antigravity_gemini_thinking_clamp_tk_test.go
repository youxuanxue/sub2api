package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeAntigravityNativeGeminiRequest_ArgonIllegalBudgetDroppedNotBoosted(t *testing.T) {
	// Negative: illegal/oversized budget must NOT become thinkingLevel=high.
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":65536}}}`)
	out := normalizeAntigravityNativeGeminiRequest("gemini-4-argon", body)

	require.False(t, gjson.GetBytes(out, thinkingBudgetPath).Exists(), "illegal budget must be dropped")
	require.False(t, gjson.GetBytes(out, thinkingLevelPath).Exists(), "illegal budget must not rewrite to high")
	// Safety inject because thinkingConfig remains but is empty of level/budget —
	// empty thinkingConfig should not inject. After drop, only empty object may remain.
	tc := gjson.GetBytes(out, thinkingConfigPath)
	require.True(t, tc.Exists())
	require.False(t, gjson.GetBytes(out, generationMaxOutputPath).Exists(),
		"empty thinkingConfig after drop must not inject maxOutputTokens")
}

func TestNormalizeAntigravityNativeGeminiRequest_ArgonLegalBudgetToLevel(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":4000}}}`)
	out := normalizeAntigravityNativeGeminiRequest("gemini-4-argon", body)

	require.False(t, gjson.GetBytes(out, thinkingBudgetPath).Exists())
	require.Equal(t, "medium", gjson.GetBytes(out, thinkingLevelPath).String())
	require.Equal(t, int64(antigravityGeminiArgonMaxOutputTokensCap), gjson.GetBytes(out, generationMaxOutputPath).Int(),
		"thinkingConfig + unset maxOutputTokens injects argon hard cap")
}

func TestNormalizeAntigravityNativeGeminiRequest_ArgonClampsOversizedMaxOutputTokens(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":65536,"thinkingConfig":{"thinkingLevel":"medium"}}}`)
	out := normalizeAntigravityNativeGeminiRequest("gemini-4-argon", body)

	require.Equal(t, "medium", gjson.GetBytes(out, thinkingLevelPath).String())
	require.Equal(t, int64(antigravityGeminiArgonMaxOutputTokensCap), gjson.GetBytes(out, generationMaxOutputPath).Int())
}

func TestNormalizeAntigravityNativeGeminiRequest_ArgonInjectsCapWhenThinkingUnsetMax(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}}}`)
	out := normalizeAntigravityNativeGeminiRequest("gemini-4-argon-medium", body)

	require.Equal(t, int64(antigravityGeminiArgonMaxOutputTokensCap), gjson.GetBytes(out, generationMaxOutputPath).Int())
	require.Equal(t, "medium", gjson.GetBytes(out, thinkingLevelPath).String())
}

func TestNormalizeAntigravityNativeGeminiRequest_NonArgonUnaffected(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":70000}}}`)
	out := normalizeAntigravityNativeGeminiRequest("gemini-2.5-flash", body)

	// Budget model: clamp to 65535, no level rewrite, no maxOutputTokens inject.
	require.Equal(t, int64(antigravityGeminiThinkingBudgetMax), gjson.GetBytes(out, thinkingBudgetPath).Int())
	require.False(t, gjson.GetBytes(out, thinkingLevelPath).Exists())
	require.False(t, gjson.GetBytes(out, generationMaxOutputPath).Exists())
}

func TestNormalizeAntigravityNativeGeminiRequest_NonArgonNoThinkingNoInject(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"temperature":0.2}}`)
	out := normalizeAntigravityNativeGeminiRequest("gemini-4-argon", body)

	require.False(t, gjson.GetBytes(out, generationMaxOutputPath).Exists(),
		"must not inject maxOutputTokens when user did not open thinking")
	require.JSONEq(t, string(body), string(out))
}

func TestNormalizeAntigravityNativeGeminiRequest_PreservesDynamicMinusOneOnBudgetModel(t *testing.T) {
	body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`)
	out := normalizeAntigravityNativeGeminiRequest("gemini-2.5-pro", body)
	require.Equal(t, int64(-1), gjson.GetBytes(out, thinkingBudgetPath).Int())
}

func TestNormalizeAntigravityNativeGeminiRequest_ClampsBelowMinusOne(t *testing.T) {
	body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-9}}}`)
	out := normalizeAntigravityNativeGeminiRequest("gemini-2.5-flash", body)
	require.Equal(t, int64(-1), gjson.GetBytes(out, thinkingBudgetPath).Int())
}

func TestNormalizeAntigravityNativeGeminiRequest_ArgonKeepsLevelDropsBudget(t *testing.T) {
	body := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low","thinkingBudget":24576}}}`)
	out := normalizeAntigravityNativeGeminiRequest("gemini-4-argon", body)
	require.Equal(t, "low", gjson.GetBytes(out, thinkingLevelPath).String())
	require.False(t, gjson.GetBytes(out, thinkingBudgetPath).Exists())
	require.Equal(t, int64(antigravityGeminiArgonMaxOutputTokensCap), gjson.GetBytes(out, generationMaxOutputPath).Int())
}

func TestAntigravityGeminiMaxOutputTokensCapForModel(t *testing.T) {
	require.Equal(t, antigravityGeminiArgonMaxOutputTokensCap, antigravityGeminiMaxOutputTokensCapForModel("gemini-4-argon"))
	require.Equal(t, antigravityGeminiArgonMaxOutputTokensCap, antigravityGeminiMaxOutputTokensCapForModel("models/gemini-4-argon-high"))
	require.Equal(t, 0, antigravityGeminiMaxOutputTokensCapForModel("gemini-3.8-flash"))
	require.Equal(t, 0, antigravityGeminiMaxOutputTokensCapForModel("claude-sonnet-4"))
}

func TestIllegalArgonBudgetDoesNotResolveToHighVariant(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gemini-4-argon":        "gemini-4-argon-medium",
				"gemini-4-argon-low":    "gemini-4-argon-low",
				"gemini-4-argon-medium": "gemini-4-argon-medium",
				"gemini-4-argon-high":   "gemini-4-argon-high",
			},
		},
	}
	raw := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":999999}}}`)
	normalized := normalizeAntigravityNativeGeminiRequest("gemini-4-argon", raw)
	got, matched := resolveGeminiThinkingVariant(account, "gemini-4-argon", normalized)
	require.False(t, matched, "dropped illegal budget must leave floor remap, got %q", got)
}
