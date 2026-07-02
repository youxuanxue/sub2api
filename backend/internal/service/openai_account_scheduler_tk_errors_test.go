//go:build unit

package service

import (
	"context"
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

		err := openAICompatNoCandidateError("deepseek-chat", PlatformNewAPI, false, accounts, nil, nil)

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

		err := openAICompatNoCandidateError("deepseek-chat", PlatformNewAPI, false, accounts, nil, nil)

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

		err := openAICompatNoCandidateError("deepseek-chat", PlatformNewAPI, false, accounts, map[int64]struct{}{39: {}}, nil)

		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrUnsupportedModel))
	})

	t.Run("empty_model_never_unsupported", func(t *testing.T) {
		accounts := []Account{newapiAcctWithMapping(39, "deepseek-v4-pro")}

		err := openAICompatNoCandidateError("", PlatformNewAPI, false, accounts, nil, nil)

		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrUnsupportedModel))
	})

	t.Run("openai_platform_passthrough_account_supports_all_models", func(t *testing.T) {
		// An account with no model_mapping (passthrough) serves every name →
		// never unsupported; the model is forwarded and any upstream 404 is handled
		// by the response-path unsupported-model classifier (path B).
		accounts := []Account{{ID: 9, Platform: PlatformOpenAI}}

		err := openAICompatNoCandidateError("gpt-9-turbo", "", false, accounts, nil, nil)

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

func TestCollectOpenAICompatSelectionFailureStatsForRequest_UpstreamChannelRestriction(t *testing.T) {
	ctx := context.Background()
	groupID := int64(18001)
	channel := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{groupID},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformNewAPI, Models: []string{"qwen-max"}},
		},
	}
	svc, _ := newSchedFixtureWithChannel(t, groupID, PlatformNewAPI, []*Account{newAPIAccount(18011, 7)}, channel)

	// Passthrough account: IsModelSupported is true, but upstream channel pricing
	// excludes gpt-5.4-mini — must classify as model_unsupported (client fault).
	stats := svc.collectOpenAICompatSelectionFailureStatsForRequest(
		ctx,
		&groupID,
		"gpt-5.4-mini",
		false,
		[]Account{*newAPIAccount(18011, 7)},
		nil,
	)
	require.Equal(t, 1, stats.ModelUnsupported)
	require.Equal(t, 0, stats.Unschedulable)
	require.True(t, tkSelectionFailedDueToUnsupportedModel(stats))
}

func TestOpenAICompatNoCandidateError_UpstreamChannelRestrictionReturnsUnsupported(t *testing.T) {
	ctx := context.Background()
	groupID := int64(18002)
	channel := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{groupID},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformNewAPI, Models: []string{"qwen-max"}},
		},
	}
	svc, _ := newSchedFixtureWithChannel(t, groupID, PlatformNewAPI, []*Account{newAPIAccount(18021, 7)}, channel)

	err := openAICompatNoCandidateError(
		"gpt-5.4-mini",
		PlatformNewAPI,
		false,
		[]Account{*newAPIAccount(18021, 7)},
		nil,
		&openAICompatNoCandidateEval{ctx: ctx, svc: svc, groupID: &groupID},
	)
	require.ErrorIs(t, err, ErrUnsupportedModel)
	assert.NotContains(t, err.Error(), "no available accounts")
}

func TestSelectAccountWithScheduler_QwenGroupWrongModelReturnsUnsupported(t *testing.T) {
	ctx := context.Background()
	groupID := int64(18003)
	qwenOnly := newapiAcctWithMapping(18031, "qwen-max", "qwen-plus")
	channel := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{groupID},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceRequested,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformNewAPI, Models: []string{"qwen-max", "qwen-plus"}},
		},
	}
	svc, _ := newSchedFixtureWithChannel(t, groupID, PlatformNewAPI, []*Account{&qwenOnly}, channel)

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"",
		"",
		"gpt-5.4-mini",
		nil,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityChatCompletions,
		false,
	)
	require.Error(t, err)
	require.True(t, selection == nil || selection.Account == nil)
	require.ErrorIs(t, err, ErrUnsupportedModel, "wrong model on Qwen-only group must be client-owned 400, not routing 429")
}

func TestSelectAccountWithLoadAwareness_QwenGroupWrongModelAliasReturnsUnsupported(t *testing.T) {
	ctx := context.Background()
	groupID := int64(18004)
	qwenOnly := newapiAcctWithMapping(18041, "qwen-max", "qwen-plus")
	channel := Channel{
		ID:                 1,
		Status:             StatusActive,
		GroupIDs:           []int64{groupID},
		RestrictModels:     true,
		BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{
			{Platform: PlatformNewAPI, Models: []string{"qwen-max", "qwen-plus"}},
		},
	}
	svc, _ := newSchedFixtureWithChannel(t, groupID, PlatformNewAPI, []*Account{&qwenOnly}, channel)
	svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true

	selection, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "", "gpt5.4-mini", nil)
	require.Error(t, err)
	require.True(t, selection == nil || selection.Account == nil)
	require.ErrorIs(t, err, ErrUnsupportedModel, "load-awareness path must classify wrong-model as client 400")
	assert.NotContains(t, err.Error(), "no available accounts")
}

func TestTkGroupUnsupportedModelCacheKey_AliasSpelling(t *testing.T) {
	t.Parallel()
	require.Equal(t, tkGroupUnsupportedModelCacheKey(18, "gpt5.4-mini"), tkGroupUnsupportedModelCacheKey(18, "gpt-5.4-mini"))
}
