package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkIsDeprecatedOpenAIModel(t *testing.T) {
	for model, wantReplacement := range tkDeprecatedOpenAIModels {
		t.Run(model, func(t *testing.T) {
			replacement, deprecated := tkIsDeprecatedOpenAIModel(model)
			require.True(t, deprecated, "deprecated table key %q must resolve", model)
			require.Equal(t, wantReplacement, replacement)
		})
	}

	servableOpenAI := ServableClientFacingIDs(context.Background(), PlatformOpenAI, nil, nil)
	require.NotEmpty(t, servableOpenAI, "openai servable SSOT must be populated")
	for _, model := range servableOpenAI {
		replacement, deprecated := tkIsDeprecatedOpenAIModel(model)
		require.False(t, deprecated, "servable openai model %q must not be deprecated", model)
		require.Empty(t, replacement)
	}

	for _, model := range []string{
		"gpt-5.3-codex",
		"gpt-5-codex",
		"gpt-5.3-chat-latest",
		"",
		"claude-sonnet-4-6",
	} {
		replacement, deprecated := tkIsDeprecatedOpenAIModel(model)
		require.False(t, deprecated, "non-deprecated example %q must pass", model)
		require.Empty(t, replacement)
	}
}

func TestTkLookupDeprecatedOpenAIModel(t *testing.T) {
	for model, wantReplacement := range tkDeprecatedOpenAIModels {
		matched, replacement, ok := TkLookupDeprecatedOpenAIModel(model)
		require.True(t, ok, "deprecated table key %q must resolve", model)
		require.Equal(t, model, matched)
		require.Equal(t, wantReplacement, replacement)
	}

	// gpt-5.2-pro is silently rewritten to gpt-5.2 by the routing-alias substring
	// fallback (openai_model_alias.go) before selection failure is observed — the
	// lookup must catch the routed form too, mirroring the anthropic raw+normalized
	// lookup order.
	matched, replacement, ok := TkLookupDeprecatedOpenAIModel(CanonicalizeOpenAICompatRoutingModel("gpt-5.2-pro"))
	require.True(t, ok)
	require.Equal(t, "gpt-5.2", matched)
	require.Equal(t, "gpt-5.5", replacement)

	_, _, ok = TkLookupDeprecatedOpenAIModel(CanonicalizeOpenAICompatRoutingModel("gpt-5.3-codex-xhigh"))
	require.False(t, ok)

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

func TestTkDeprecatedOpenAISelectionFailure(t *testing.T) {
	err := tkDeprecatedOpenAISelectionFailure("gpt-5.2")
	require.ErrorIs(t, err, ErrDeprecatedOpenAIModel)
	require.NotContains(t, strings.ToLower(err.Error()), "no available accounts")

	err = tkDeprecatedOpenAISelectionFailure("codex-auto-review")
	require.ErrorIs(t, err, ErrDeprecatedOpenAIModel)

	err = tkDeprecatedOpenAISelectionFailure("gpt-5-codex")
	require.NoError(t, err)

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
