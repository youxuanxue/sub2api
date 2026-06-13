//go:build unit

package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newapiAcctWithMapping builds a minimal newapi Account whose model_mapping
// allowlist contains exactly the given served model names (value == key, the
// identity mapping prod uses for deepseek-v4-* / qwen3.7-*).
func newapiAcctWithMapping(id int64, served ...string) Account {
	mapping := make(map[string]any, len(served))
	for _, m := range served {
		mapping[m] = m
	}
	return Account{
		ID:          id,
		Platform:    PlatformNewAPI,
		Credentials: map[string]any{"model_mapping": mapping},
	}
}

// TestOpenAICompatNoCandidateError is the core of the 2026-06-13 fix: when the
// schedulable pool was emptied PURELY because no account serves the requested
// model NAME, the OpenAI-compat selection paths must surface ErrUnsupportedModel
// (→ HTTP 400), not an empty-pool 429. The same function backs both the
// load-balance scheduler and the selectBestAccount (count_tokens/sticky) path.
func TestOpenAICompatNoCandidateError(t *testing.T) {
	t.Run("all_accounts_lack_model_mapping_entry_returns_unsupported", func(t *testing.T) {
		// Prod shape: group 11 "deepseek" maps only deepseek-v4-*, client asks for
		// the legacy "deepseek-chat".
		accounts := []Account{
			newapiAcctWithMapping(39, "deepseek-v4-pro", "deepseek-v4-flash"),
		}

		err := openAICompatNoCandidateError("deepseek-chat", PlatformNewAPI, false, accounts, nil)

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrUnsupportedModel), "expected ErrUnsupportedModel, got %v", err)
		assert.False(t, errors.Is(err, ErrNoAvailableAccounts))
		// Must NOT carry the phrase isOpsNoAvailableAccountError matches, or ops
		// would relabel it as routing-capacity instead of a client request error.
		assert.NotContains(t, err.Error(), "no available accounts")
	})

	t.Run("a_supporting_account_present_stays_no_available_accounts", func(t *testing.T) {
		// One account DOES serve the model (it just got filtered downstream, e.g.
		// cooldown/capability) → this is a transient capacity gap, keep 429 family.
		accounts := []Account{
			newapiAcctWithMapping(39, "deepseek-v4-pro"),
			newapiAcctWithMapping(40, "deepseek-chat"),
		}

		err := openAICompatNoCandidateError("deepseek-chat", PlatformNewAPI, false, accounts, nil)

		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrUnsupportedModel))
		// newapi platform label preserved for operator clarity.
		assert.Contains(t, err.Error(), "newapi")
	})

	t.Run("excluded_supporting_account_does_not_trigger_unsupported", func(t *testing.T) {
		// The only account serves the model but was excluded (failover retry). We
		// must NOT claim "unsupported" — fall back to the 429 family.
		accounts := []Account{
			newapiAcctWithMapping(39, "deepseek-chat"),
		}

		err := openAICompatNoCandidateError("deepseek-chat", PlatformNewAPI, false, accounts, map[int64]struct{}{39: {}})

		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrUnsupportedModel))
	})

	t.Run("empty_model_never_unsupported", func(t *testing.T) {
		accounts := []Account{newapiAcctWithMapping(39, "deepseek-v4-pro")}

		err := openAICompatNoCandidateError("", PlatformNewAPI, false, accounts, nil)

		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrUnsupportedModel))
	})

	t.Run("openai_platform_passthrough_account_supports_all_models", func(t *testing.T) {
		// An account with no model_mapping (passthrough) serves every name →
		// never unsupported; the model is forwarded and any upstream 404 is handled
		// by the response-path unsupported-model classifier (path B).
		accounts := []Account{{ID: 9, Platform: PlatformOpenAI}}

		err := openAICompatNoCandidateError("gpt-9-turbo", "", false, accounts, nil)

		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrUnsupportedModel))
	})
}

// TestCollectOpenAICompatSelectionFailureStats pins the categorization the
// unsupported-model predicate depends on.
func TestCollectOpenAICompatSelectionFailureStats(t *testing.T) {
	t.Run("all_unsupported", func(t *testing.T) {
		stats := collectOpenAICompatSelectionFailureStats(
			[]Account{newapiAcctWithMapping(1, "deepseek-v4-pro"), newapiAcctWithMapping(2, "deepseek-v4-flash")},
			"deepseek-chat", nil,
		)
		assert.Equal(t, 2, stats.ModelUnsupported)
		assert.Equal(t, 0, stats.Unschedulable)
		assert.True(t, tkSelectionFailedDueToUnsupportedModel(stats))
	})

	t.Run("mixed_supporting_suppresses", func(t *testing.T) {
		stats := collectOpenAICompatSelectionFailureStats(
			[]Account{newapiAcctWithMapping(1, "deepseek-v4-pro"), newapiAcctWithMapping(2, "deepseek-chat")},
			"deepseek-chat", nil,
		)
		assert.Equal(t, 1, stats.ModelUnsupported)
		assert.Equal(t, 1, stats.Unschedulable)
		assert.False(t, tkSelectionFailedDueToUnsupportedModel(stats))
	})
}
