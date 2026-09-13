//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func candidateDiscoveryFixture(groups []Group, accounts []Account) (*UniversalCapabilityService, *APIKey) {
	resolver, _, key := globalCandidateFixture(groups, accounts)
	return &UniversalCapabilityService{entitlements: resolver.lister, resolver: resolver,
		fallback: func(_ context.Context, platform string) ([]string, error) {
			if platform == PlatformOpenAI {
				return []string{"gpt-5.4"}, nil
			}
			return nil, nil
		}}, key
}

func TestUS050_CandidateDiscoveryUsesActualAccountAndNativePlan(t *testing.T) {
	group := grp(1, PlatformOpenAI, 99, false)
	account := globalCandidateAccount(115, 1, 1)
	account.Platform, account.ChannelType = PlatformNewAPI, 14
	account.Credentials["model_mapping"] = map[string]any{"claude-fable-5": "claude-fable-5"}
	attachTestProtocolCapability(&account, protocolrouter.ProtocolMessages)
	for _, universal := range []bool{false, true} {
		svc, key := candidateDiscoveryFixture([]Group{group}, []Account{account})
		if !universal {
			key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &group, &group.ID
		}
		models, err := svc.List(context.Background(), key, UniversalProtocolAnthropic)
		require.NoError(t, err)
		require.Len(t, models, 1)
		require.Equal(t, "claude-fable-5", models[0].ID)
		require.Equal(t, int64(1), models[0].SelectedGroup.ID)
		if universal {
			require.Nil(t, key.Group, "discovery never binds the request key")
		}
	}
}

func TestUS050_CandidateDiscoveryRejectsConflictingOriginButKeepsPeer(t *testing.T) {
	groups := []Group{grp(10, PlatformAnthropic, -100, false), grp(20, PlatformGemini, 100, false), grp(30, PlatformGrok, 0, false)}
	price := 3.0
	groups[1].ModelPricing = []ChannelModelPricing{{Models: []string{"gpt-5.4"}, InputPrice: &price}}
	conflicted := globalCandidateAccount(115, 1, 10, 20)
	peer := globalCandidateAccount(124, 10, 30)
	svc, key := candidateDiscoveryFixture(groups, []Account{conflicted, peer})
	models, accounts, err := svc.DiscoverCandidates(context.Background(), key, UniversalProtocolOpenAI)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, int64(30), models[0].SelectedGroup.ID)
	require.Len(t, accounts, 1)
	require.Equal(t, int64(124), accounts[0].ID)

	svc, key = candidateDiscoveryFixture(groups, []Account{conflicted})
	_, err = svc.List(context.Background(), key, UniversalProtocolOpenAI)
	require.ErrorIs(t, err, ErrCandidatePolicyConflict)
}

func TestUS050_CandidateDiscoveryComparesPoliciesAgainstRepresentativeRequest(t *testing.T) {
	groups := []Group{grp(10, PlatformAnthropic, 0, false), grp(20, PlatformGemini, 0, false)}
	groups[0].MaxReasoningEffort, groups[1].MaxReasoningEffort = "low", "high"
	svc, key := candidateDiscoveryFixture(groups, []Account{globalCandidateAccount(115, 1, 10, 20)})
	staleBody := []byte(`{"model":"gpt-5.4","reasoning_effort":"high","messages":[{"role":"user","content":"old request"}]}`)
	ctx := svc.resolver.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", staleBody)
	models, err := svc.List(ctx, key, UniversalProtocolCodex)
	require.NoError(t, err, "an inactive reasoning policy does not conflict for a request without reasoning")
	require.Len(t, models, 1)
}

func TestUS050_CandidateDiscoveryKeepsLegalProtocolAfterAnotherPolicyConflict(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 0, false), grp(20, PlatformOpenAI, 0, false)}
	groups[0].AllowMessagesDispatch, groups[1].AllowMessagesDispatch = true, true
	on, off := true, false
	groups[0].MessagesCompactionEnabled, groups[1].MessagesCompactionEnabled = &on, &off
	threshold := 1
	groups[0].MessagesCompactionInputTokensThreshold = &threshold
	svc, key := candidateDiscoveryFixture(groups, []Account{globalCandidateAccount(115, 1, 10, 20)})
	models, err := svc.List(context.Background(), key, UniversalProtocolAll)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Contains(t, models[0].Protocols, UniversalProtocolOpenAI)
	require.NotContains(t, models[0].Protocols, UniversalProtocolAnthropic)
}

func TestUS050_CandidateDiscoverySeparatesPaymentTiersAndIgnoresRuntimeCapacity(t *testing.T) {
	groups := []Group{grp(10, PlatformAnthropic, 0, true), grp(20, PlatformGemini, 0, false)}
	price := 3.0
	groups[0].ModelPricing = []ChannelModelPricing{{Models: []string{"gpt-5.4"}, InputPrice: &price}}
	account := globalCandidateAccount(115, 1, 10, 20)
	reset := time.Now().Add(time.Hour)
	account.RateLimitResetAt, account.Schedulable = &reset, false
	svc, key := candidateDiscoveryFixture(groups, []Account{account})
	key.User.Balance = 0
	models, err := svc.List(context.Background(), key, UniversalProtocolOpenAI)
	require.NoError(t, err)
	require.Len(t, models, 1, "support discovery is independent of payment admission and temporary availability")
}

func TestUS050_CandidateDiscoveryPreservesDirectMappingOnly(t *testing.T) {
	group := grp(10, PlatformOpenAI, 0, false)
	group.AllowMessagesDispatch = true
	group.MessagesDispatchModelConfig.ExactModelMappings = map[string]string{"legacy-client-model": "gpt-5.4"}
	for _, universal := range []bool{false, true} {
		svc, key := candidateDiscoveryFixture([]Group{group}, []Account{globalCandidateAccount(115, 1, 10)})
		catalog := NewPricingCatalogService(nil)
		svc.modelFilter = NewModelListFilter(catalog, nil)
		if !universal {
			key.RoutingMode, key.Group, key.GroupID = RoutingModeDirect, &group, &group.ID
		}
		models, err := svc.List(context.Background(), key, UniversalProtocolOpenAI)
		require.NoError(t, err)
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID)
		}
		if universal {
			require.NotContains(t, ids, "legacy-client-model")
		} else {
			require.Contains(t, ids, "legacy-client-model")
		}
	}
}

func TestUS050_CandidateDiscoveryForcedPlatformUsesAccountNotGroup(t *testing.T) {
	groups := []Group{grp(10, PlatformAnthropic, 0, false), grp(20, PlatformOpenAI, 0, false)}
	account := globalCandidateAccount(115, 1, 10)
	attachTestProtocolCapability(&account, protocolrouter.ProtocolResponses)
	unrelated := globalCandidateAccount(124, 1, 20)
	unrelated.Platform = PlatformAnthropic
	svc, key := candidateDiscoveryFixture(groups, []Account{account, unrelated})
	models, accounts, err := svc.DiscoverCandidates(context.Background(), key, UniversalProtocolCodex)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, int64(10), models[0].SelectedGroup.ID)
	require.Len(t, accounts, 1)
	require.Equal(t, int64(115), accounts[0].ID)

	ctx := context.WithValue(context.Background(), ctxkey.ForcePlatform, PlatformGemini)
	models, err = svc.List(ctx, key, UniversalProtocolCodex)
	require.NoError(t, err)
	require.Empty(t, models)
}

func TestUS050_CandidateDiscoveryPropagatesPricingReadFailure(t *testing.T) {
	svc, key := candidateDiscoveryFixture([]Group{grp(10, PlatformOpenAI, 0, false)}, []Account{globalCandidateAccount(115, 1, 10)})
	failure := errors.New("channel database unavailable")
	svc.resolver.candidateGateway.channelService = NewChannelService(&mockChannelRepository{
		listAllFn: func(context.Context) ([]Channel, error) { return nil, failure },
	}, nil, nil, nil, nil)
	_, err := svc.List(context.Background(), key, UniversalProtocolOpenAI)
	require.ErrorIs(t, err, failure)
}

func TestCandidateDiscoveryKeepsVerifiedRouteWhenOtherProtocolIsUnknown(t *testing.T) {
	group := grpNoImage(10, PlatformOpenAI, 0, false)
	healthy := globalCandidateAccount(1, 1, 10)
	unknown := globalCandidateAccount(2, 1, 10)
	unknown.Credentials[openAIEndpointCapabilitiesCredentialKey] = []string{"chat_completions"}
	unknown.ProtocolEndpointCapability.SupportedProtocols = nil
	unknown.Schedulable = false
	svc, key := candidateDiscoveryFixture([]Group{group}, []Account{healthy, unknown})
	models, accounts, err := svc.DiscoverCandidates(context.Background(), key, UniversalProtocolAll)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Contains(t, models[0].Protocols, UniversalProtocolOpenAI)
	require.Len(t, accounts, 1)
	require.Equal(t, healthy.ID, accounts[0].ID)
}

func TestCandidateDiscoveryPropagatesUnknownWhenNoVerifiedRouteExists(t *testing.T) {
	unknown := globalCandidateAccount(1, 1, 10)
	unknown.Credentials[openAIEndpointCapabilitiesCredentialKey] = []string{"chat_completions"}
	unknown.ProtocolEndpointCapability.SupportedProtocols = nil
	svc, key := candidateDiscoveryFixture([]Group{grpNoImage(10, PlatformOpenAI, 0, false)}, []Account{unknown})
	_, _, err := svc.DiscoverCandidates(context.Background(), key, UniversalProtocolAll)
	require.ErrorIs(t, err, ErrProtocolCapabilityUnknown)
}

func TestCandidateDiscoveryKeepsVerifiedModelsWhenDifferentModelIsUnknown(t *testing.T) {
	for _, unknownModel := range []string{"aaa-unknown-model", "zzz-unknown-model"} {
		t.Run(unknownModel, func(t *testing.T) {
			healthy := globalCandidateAccount(1, 1, 10)
			healthy.Credentials["model_mapping"] = map[string]any{"gpt-5.4": "gpt-5.4"}
			unknown := globalCandidateAccount(2, 1, 10)
			unknown.Credentials["model_mapping"] = map[string]any{unknownModel: unknownModel}
			unknown.Credentials[openAIEndpointCapabilitiesCredentialKey] = []string{"chat_completions"}
			unknown.ProtocolEndpointCapability.SupportedProtocols = nil
			svc, key := candidateDiscoveryFixture([]Group{grpNoImage(10, PlatformOpenAI, 0, false)}, []Account{healthy, unknown})
			models, accounts, err := svc.DiscoverCandidates(context.Background(), key, UniversalProtocolAll)
			require.NoError(t, err)
			require.Len(t, models, 1)
			require.Equal(t, "gpt-5.4", models[0].ID)
			require.Len(t, accounts, 1)
			require.Equal(t, healthy.ID, accounts[0].ID)
		})
	}
}
