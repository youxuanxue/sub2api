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
		{"gpt-5.2 deprecated -> gpt-5.5", "gpt-5.2", true, "gpt-5.5"},
		{"gpt-5.2-pro deprecated -> gpt-5.5", "gpt-5.2-pro", true, "gpt-5.5"},
		{"gpt-5.3-codex deprecated -> spark", "gpt-5.3-codex", true, "gpt-5.3-codex-spark"},
		{"gpt-5-codex deprecated -> spark", "gpt-5-codex", true, "gpt-5.3-codex-spark"},
		{"codex-auto-review deprecated -> no replacement", "codex-auto-review", true, ""},

		{"gpt-5.4 current passes", "gpt-5.4", false, ""},
		{"gpt-5.5 current passes", "gpt-5.5", false, ""},
		{"gpt-5.3-codex-spark current passes", "gpt-5.3-codex-spark", false, ""},
		{"gpt-5.3-chat-latest current passes", "gpt-5.3-chat-latest", false, ""},
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

func TestTkLookupDeprecatedOpenAIModel(t *testing.T) {
	matched, replacement, ok := TkLookupDeprecatedOpenAIModel("gpt-5.2")
	require.True(t, ok)
	require.Equal(t, "gpt-5.2", matched)
	require.Equal(t, "gpt-5.5", replacement)

	// gpt-5.2-pro is silently rewritten to gpt-5.2 by the routing-alias substring
	// fallback (openai_model_alias.go) before selection failure is observed — the
	// lookup must catch the routed form too, mirroring the anthropic raw+normalized
	// lookup order.
	matched, replacement, ok = TkLookupDeprecatedOpenAIModel(CanonicalizeOpenAICompatRoutingModel("gpt-5.2-pro"))
	require.True(t, ok)
	require.Equal(t, "gpt-5.2-pro", matched)
	require.Equal(t, "gpt-5.5", replacement)

	matched, replacement, ok = TkLookupDeprecatedOpenAIModel(CanonicalizeOpenAICompatRoutingModel("gpt-5.3-codex-xhigh"))
	require.True(t, ok)
	require.Equal(t, "gpt-5.3-codex", matched)
	require.Equal(t, "gpt-5.3-codex-spark", replacement)

	_, _, ok = TkLookupDeprecatedOpenAIModel("gpt-5.4")
	require.False(t, ok)

	_, _, ok = TkLookupDeprecatedOpenAIModel("")
	require.False(t, ok)
}

func TestTkBuildDeprecatedOpenAIModelMessage(t *testing.T) {
	msg := TkBuildDeprecatedOpenAIModelMessage("gpt-5.2", "gpt-5.5")
	require.Contains(t, msg, "gpt-5.2", "must echo the requested model")
	require.Contains(t, msg, "gpt-5.5", "must suggest the replacement")
	require.True(t, strings.Contains(msg, "retired") || strings.Contains(msg, "deprecated"),
		"message should explain why the model is rejected")

	msg = TkBuildDeprecatedOpenAIModelMessage("codex-auto-review", "")
	require.Contains(t, msg, "codex-auto-review")
	require.Contains(t, msg, "not directly selectable")
}

// Guards against silent table edits during upstream merges / future PRs.
func TestTkDeprecatedOpenAIModelsTableIsExhaustive(t *testing.T) {
	expected := map[string]struct{}{
		"gpt-5.2":           {},
		"gpt-5.2-pro":       {},
		"gpt-5.3-codex":     {},
		"gpt-5-codex":       {},
		"codex-auto-review": {},
	}
	require.Len(t, tkDeprecatedOpenAIModels, len(expected),
		"deprecated openai model table size changed; update this assertion intentionally")
	for id := range expected {
		_, ok := tkDeprecatedOpenAIModels[id]
		require.Truef(t, ok, "deprecated model %q must remain on the gate list", id)
	}
}

func TestTkDeprecatedOpenAISelectionFailure(t *testing.T) {
	err := tkDeprecatedOpenAISelectionFailure("gpt-5.2")
	require.ErrorIs(t, err, ErrDeprecatedOpenAIModel)
	require.NotContains(t, strings.ToLower(err.Error()), "no available accounts")

	err = tkDeprecatedOpenAISelectionFailure("codex-auto-review")
	require.ErrorIs(t, err, ErrDeprecatedOpenAIModel)

	err = tkDeprecatedOpenAISelectionFailure("gpt-5-codex")
	require.ErrorIs(t, err, ErrDeprecatedOpenAIModel)

	err = tkDeprecatedOpenAISelectionFailure("gpt-5.4")
	require.NoError(t, err)
}

func TestOpenAICompatNoCandidateError_DeprecatedModelBeatsUnsupportedAndEmptyPool(t *testing.T) {
	// Deprecated check must fire before the generic unsupported-model / empty-pool
	// branches, even when the account pool is genuinely empty (accounts == nil).
	err := openAICompatNoCandidateError("gpt-5.2", PlatformOpenAI, false, nil, nil, nil)
	require.ErrorIs(t, err, ErrDeprecatedOpenAIModel)
	require.False(t, errors.Is(err, ErrUnsupportedModel))
	require.False(t, errors.Is(err, ErrNoAvailableAccounts))

	err = openAICompatNoCandidateError("gpt-5.4", PlatformOpenAI, false, nil, nil, nil)
	require.False(t, errors.Is(err, ErrDeprecatedOpenAIModel))
}

func TestNoAvailableOpenAISelectionError_DeprecatedModelBeatsEmptyPool(t *testing.T) {
	// The two direct call sites in openai_account_scheduler.go (len(accounts)==0
	// fast paths) route through this shared helper, so the deprecated gate must
	// live here too, not just in openAICompatNoCandidateError.
	err := noAvailableOpenAISelectionError("codex-auto-review", false, PlatformOpenAI)
	require.ErrorIs(t, err, ErrDeprecatedOpenAIModel)

	err = noAvailableOpenAISelectionError("gpt-5.5", false, PlatformOpenAI)
	require.False(t, errors.Is(err, ErrDeprecatedOpenAIModel))
	require.False(t, errors.Is(err, ErrDeprecatedOpenAIModel))
}
