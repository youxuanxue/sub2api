package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkIsDeprecatedOpenAIModel(t *testing.T) {
	cases := []struct {
		name           string
		model          string
		wantDeprecated bool
		wantReplaceTo  string
	}{
		{"gpt-5.2 retired -> gpt-5.5", "gpt-5.2", true, "gpt-5.5"},
		{"gpt-5.2-pro retired -> gpt-5.5-pro", "gpt-5.2-pro", true, "gpt-5.5-pro"},
		{"gpt-5.3-codex retired -> gpt-5.3-codex-spark", "gpt-5.3-codex", true, "gpt-5.3-codex-spark"},
		{"gpt-5-codex retired -> gpt-5.3-codex-spark", "gpt-5-codex", true, "gpt-5.3-codex-spark"},
		{"codex-auto-review never selectable, empty replacement", "codex-auto-review", true, ""},

		{"gpt-5.4 current passes", "gpt-5.4", false, ""},
		{"gpt-5.5 current passes", "gpt-5.5", false, ""},
		{"gpt-5.3-codex-spark current passes", "gpt-5.3-codex-spark", false, ""},
		{"empty string passes", "", false, ""},
		{"non-openai model passes", "claude-sonnet-4-6", false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replacement, deprecated := tkIsDeprecatedOpenAIModel(tc.model)
			require.Equal(t, tc.wantDeprecated, deprecated, "deprecated flag for %q", tc.model)
			if tc.wantDeprecated {
				require.Equal(t, tc.wantReplaceTo, replacement)
			} else {
				require.Empty(t, replacement)
			}
		})
	}
}

func TestTkLookupDeprecatedOpenAIModel_NormalizedSpelling(t *testing.T) {
	// canonicalizeOpenAIModelAliasSpelling collapses wire spellings such as
	// gpt5.2 -> gpt-5.2 before the table lookup runs.
	matched, replacement, ok := TkLookupDeprecatedOpenAIModel("gpt5.2")
	require.True(t, ok)
	require.Equal(t, "gpt-5.2", matched)
	require.Equal(t, "gpt-5.5", replacement)
}

func TestTkBuildDeprecatedOpenAIModelMessage(t *testing.T) {
	t.Run("version_retirement_branch", func(t *testing.T) {
		msg := TkBuildDeprecatedOpenAIModelMessage("gpt-5.2", "gpt-5.5")
		require.Contains(t, msg, "gpt-5.2", "must echo the requested model")
		require.Contains(t, msg, "gpt-5.5", "must suggest the replacement")
		require.Contains(t, msg, "retired")
	})

	t.Run("never_selectable_branch", func(t *testing.T) {
		msg := TkBuildDeprecatedOpenAIModelMessage("codex-auto-review", "")
		require.Contains(t, msg, "codex-auto-review")
		require.Contains(t, msg, "not directly selectable")
		require.NotContains(t, msg, "retired")
	})
}

// Guards against silent table edits during upstream merges. The set of
// retired/non-selectable IDs is the contract the customer-facing migration
// message refers to; adding/removing one is a deliberate change that should not
// slip in alongside an unrelated diff. Update this assertion explicitly when the
// list changes.
func TestTkDeprecatedOpenAIModelsTableIsExhaustive(t *testing.T) {
	expected := map[string]struct{}{
		"gpt-5.2":           {},
		"gpt-5.2-pro":       {},
		"gpt-5.3-codex":     {},
		"gpt-5-codex":       {},
		"codex-auto-review": {},
	}
	require.Len(t, tkDeprecatedOpenAIModels, len(expected),
		"retired/non-selectable model table size changed; update this assertion intentionally")
	for id := range expected {
		_, ok := tkDeprecatedOpenAIModels[id]
		require.Truef(t, ok, "retired/non-selectable model %q must remain on the gate list", id)
	}
}

func TestTkDeprecatedOpenAISelectionFailure(t *testing.T) {
	err := tkDeprecatedOpenAISelectionFailure("gpt-5.2")
	require.ErrorIs(t, err, ErrDeprecatedOpenAIModel)
	require.NotContains(t, strings.ToLower(err.Error()), "no available accounts")

	err = tkDeprecatedOpenAISelectionFailure("codex-auto-review")
	require.ErrorIs(t, err, ErrDeprecatedOpenAIModel)
	require.Contains(t, err.Error(), "not directly selectable")

	err = tkDeprecatedOpenAISelectionFailure("gpt-5.4")
	require.NoError(t, err)
}

func TestTkDeprecatedOpenAISelectionFailure_NotConfusedWithAnthropic(t *testing.T) {
	err := tkDeprecatedOpenAISelectionFailure("claude-3-5-sonnet-20241022")
	require.NoError(t, err)
	require.False(t, errors.Is(err, ErrDeprecatedOpenAIModel))
}
