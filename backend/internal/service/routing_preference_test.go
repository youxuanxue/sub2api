//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestCandidatePathLessTokenseaPreferenceOverridesGlobalPriority(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceTokenseaFirst}
	tokensea := &candidateExecutionPath{account: &Account{ID: 136, Platform: PlatformNewAPI, Priority: 20}, model: "claude-sonnet-4-6"}
	other := &candidateExecutionPath{account: &Account{ID: 300, Platform: PlatformNewAPI, Priority: 1}, model: "claude-sonnet-4-6"}

	if !candidatePathLess(key, ShapeAnthropicMessages, nil, nil, tokensea, other) {
		t.Fatal("tokensea preference must outrank a lower global priority account")
	}
}

func TestCandidatePathLessDefaultKeepsGlobalPriority(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceDefault}
	preferred := &candidateExecutionPath{account: &Account{ID: 136, Platform: PlatformNewAPI, Priority: 20}, model: "claude-sonnet-4-6"}
	other := &candidateExecutionPath{account: &Account{ID: 300, Platform: PlatformNewAPI, Priority: 1}, model: "claude-sonnet-4-6"}

	if candidatePathLess(key, ShapeAnthropicMessages, nil, nil, preferred, other) {
		t.Fatal("default preference must preserve global priority")
	}
}

func TestCandidatePreferenceOnlyMatchesConfiguredTokenseaAccounts(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceTokenseaFirst}
	if candidatePreferenceRank(key, &Account{ID: 136, Platform: PlatformNewAPI}, "claude-sonnet-4-6") != 0 {
		t.Fatal("account 136 must be preferred")
	}
	if candidatePreferenceRank(key, &Account{ID: 218, Platform: PlatformNewAPI}, "claude-sonnet-4-6") != 0 {
		t.Fatal("account 218 must be preferred")
	}
	if candidatePreferenceRank(key, &Account{ID: 219, Platform: PlatformNewAPI}, "claude-sonnet-4-6") != 1 {
		t.Fatal("unconfigured account must remain in fallback tier")
	}
}

func TestCandidatePathLessCountTokensPrefersNativeCounter(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceTokenseaFirst}
	tokensea := &candidateExecutionPath{
		account: &Account{ID: 136, Platform: PlatformNewAPI, Priority: 1, Credentials: map[string]any{"base_url": "https://agent.tokensea.ai"}},
		model:   "claude-sonnet-4-6",
	}
	native := &candidateExecutionPath{
		account: &Account{ID: 300, Platform: PlatformAnthropic, Priority: 20},
		model:   "claude-sonnet-4-6",
	}
	if !candidatePathLess(key, ShapeAnthropicCountTokens, nil, nil, native, tokensea) {
		t.Fatal("count_tokens must prefer a native counter over the tokensea preference")
	}
	if candidatePathLess(key, ShapeAnthropicMessages, nil, nil, native, tokensea) {
		t.Fatal("messages must keep the tokensea preference ahead of a worse-priority native account")
	}
}

func TestCandidatePreferenceOnlyAppliesToNewAPIClaude(t *testing.T) {
	key := &APIKey{RoutingPreference: RoutingPreferenceTokenseaFirst}
	if candidatePreferenceRank(key, &Account{ID: 136, Platform: PlatformNewAPI}, "gpt-5.4") != 1 {
		t.Fatal("tokensea preference must not affect non-Claude models")
	}
	if candidatePreferenceRank(key, &Account{ID: 136, Platform: PlatformOpenAI}, "claude-sonnet-4-6") != 1 {
		t.Fatal("tokensea preference must not affect non-NewAPI accounts")
	}
}

// Production 136/218 are NewAPI channel 14 accounts in an Anthropic group.
// Use their actual platform and native Messages path through candidate admission.
func TestCandidateTokenseaNewAPIPreferenceAndFallback(t *testing.T) {
	for _, target := range []struct {
		id    int64
		model string
	}{
		{136, "claude-sonnet-4-6"}, {218, "claude-fable-5"},
	} {
		for _, scenario := range []string{"preferred", "default", "unhealthy", "full", "race", "excluded", "incompatible"} {
			t.Run(fmt.Sprintf("%d/%s", target.id, scenario), func(t *testing.T) {
				preferred := globalCandidateAccount(target.id, 20, 1)
				preferred.Platform, preferred.ChannelType = PlatformNewAPI, 14
				preferred.Credentials["model_mapping"] = map[string]any{target.model: target.model}
				attachTestProtocolCapability(&preferred, protocolrouter.ProtocolMessages)
				fallback := globalCandidateAccount(300, 1, 1)
				fallback.Platform = PlatformAnthropic
				fallback.Credentials["model_mapping"] = map[string]any{target.model: target.model}
				attachTestProtocolCapability(&fallback, protocolrouter.ProtocolMessages)
				if scenario == "unhealthy" {
					preferred.Status = StatusError
				}
				if scenario == "incompatible" {
					attachTestProtocolCapability(&preferred, protocolrouter.ProtocolChatCompletions)
				}
				r, _, key := globalCandidateFixture([]Group{grp(1, PlatformAnthropic, 1, false)}, []Account{preferred, fallback})
				key.RoutingPreference = RoutingPreferenceTokenseaFirst
				if scenario == "default" {
					key.RoutingPreference = RoutingPreferenceDefault
				}
				cache := schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{}}
				if scenario == "full" {
					cache.loadMap[target.id] = &AccountLoadInfo{AccountID: target.id, CurrentConcurrency: 10}
				}
				if scenario == "race" {
					cache.acquireResults = map[int64]bool{target.id: false}
				}
				r.candidateGateway.concurrencyService = NewConcurrencyService(cache)
				body := []byte(fmt.Sprintf(`{"model":%q,"max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`, target.model))
				ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", target.model, body, "", "")
				require.NoError(t, err)
				options := candidateSelectOptions{acquire: true}
				if scenario == "excluded" {
					options.excluded = map[int64]struct{}{target.id: {}}
				}
				selection, err := state.selectAccount(ctx, options)
				require.NoError(t, err)
				require.True(t, selection.Acquired)
				defer selection.ReleaseFunc()
				want := int64(300)
				if scenario == "preferred" {
					want = target.id
				}
				require.Equal(t, want, selection.Account.ID)
				require.Equal(t, protocolrouter.ProtocolMessages, selection.ProtocolPlan.TargetProtocol())
			})
		}
	}
}

func TestCandidateCountTokensPrefersNativeAccountOverTokensea(t *testing.T) {
	preferred := globalCandidateAccount(136, 1, 1)
	preferred.Platform, preferred.ChannelType = PlatformNewAPI, 14
	preferred.Credentials["base_url"] = "https://agent.tokensea.ai"
	preferred.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}
	attachTestProtocolCapability(&preferred, protocolrouter.ProtocolMessages)
	fallback := globalCandidateAccount(300, 20, 1)
	fallback.Platform = PlatformAnthropic
	fallback.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}
	attachTestProtocolCapability(&fallback, protocolrouter.ProtocolMessages)
	r, _, key := globalCandidateFixture([]Group{grp(1, PlatformAnthropic, 1, false)}, []Account{preferred, fallback})
	key.RoutingPreference = RoutingPreferenceTokenseaFirst
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
	ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicCountTokens, "/v1/messages/count_tokens", "claude-sonnet-4-6", body, "", "")
	require.NoError(t, err)
	selection, err := state.selectAccount(ctx, candidateSelectOptions{acquire: true})
	require.NoError(t, err)
	require.True(t, selection.Acquired)
	defer selection.ReleaseFunc()
	require.Equal(t, int64(300), selection.Account.ID)
}
