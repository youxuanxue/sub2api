package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type us046EntitlementStub struct {
	groups []Group
	err    error
}

type us046AccountRepoStub struct {
	AccountRepository
	accounts      []Account
	groupCalls    atomic.Int64
	platformCalls atomic.Int64
}

type us046AvailabilityRepoStub struct {
	err error
}

func (s *us046AvailabilityRepoStub) Upsert(context.Context, string, string, func(AvailabilityState) AvailabilityState) error {
	return s.err
}

func (s *us046AvailabilityRepoStub) Get(context.Context, string, string) (AvailabilityState, error) {
	return AvailabilityState{}, s.err
}

func (s *us046AccountRepoStub) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	s.groupCalls.Add(1)
	return append([]Account(nil), s.accounts...), nil
}

func (s *us046AccountRepoStub) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	s.platformCalls.Add(1)
	return append([]Account(nil), s.accounts...), nil
}

func (s *us046AccountRepoStub) ListAllWithFilters(_ context.Context, platform, _, status string, _ string, groupID int64, _ string, _ int) ([]Account, error) {
	var out []Account
	for _, acc := range s.accounts {
		if platform != "" && acc.Platform != platform {
			continue
		}
		if status != "" && acc.Status != status {
			continue
		}
		if groupID > 0 && len(acc.GroupIDs) > 0 {
			matched := false
			for _, id := range acc.GroupIDs {
				if id == groupID {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		out = append(out, acc)
	}
	return out, nil
}

func (s *us046EntitlementStub) GetAvailableGroups(context.Context, int64) ([]Group, error) {
	return s.groups, s.err
}

func us046ActiveGroup(id int64, platform string, subscription bool) Group {
	g := Group{ID: id, Name: platform, Platform: platform, Status: StatusActive, SortOrder: int(id)}
	if subscription {
		g.SubscriptionType = SubscriptionTypeSubscription
	}
	return g
}

func TestUS046_CapabilitiesOnlyListCallableModelsForProtocol(t *testing.T) {
	standard := us046ActiveGroup(10, PlatformOpenAI, false)
	subscription := us046ActiveGroup(30, PlatformOpenAI, true)
	anthropic := us046ActiveGroup(40, PlatformAnthropic, false)
	lister := &us046EntitlementStub{groups: []Group{standard, subscription, anthropic}}

	candidates := func(_ context.Context, groupID int64, _ string) ([]string, bool, error) {
		switch groupID {
		case standard.ID, subscription.ID:
			return []string{"gpt-chat", "veo-video"}, false, nil
		case anthropic.ID:
			return []string{"claude-messages"}, false, nil
		default:
			return nil, false, nil
		}
	}
	supports := func(_ context.Context, groupID int64, _ string, model string, shape UniversalShape) (bool, error) {
		switch {
		case (groupID == standard.ID || groupID == subscription.ID) && model == "gpt-chat" && shape == ShapeOpenAIChat:
			return true, nil
		case groupID == subscription.ID && model == "veo-video" && shape == ShapeOpenAIVideo:
			return true, nil
		case groupID == anthropic.ID && model == "claude-messages" && shape == ShapeAnthropicMessages:
			return true, nil
		default:
			return false, nil
		}
	}

	svc := newUniversalCapabilityService(lister, candidates, supports, nil)
	key := &APIKey{ID: 7, UserID: 9, RoutingMode: RoutingModeUniversal}
	got, err := svc.List(context.Background(), key, UniversalProtocolAll)
	require.NoError(t, err)
	require.Equal(t, []UniversalCapability{
		{
			ID:         "claude-messages",
			Protocols:  []UniversalProtocol{UniversalProtocolAnthropic},
			Modalities: []UniversalModality{UniversalModalityChat},
			Routes: []UniversalCapabilityRoute{
				{Protocol: UniversalProtocolAnthropic, Modality: UniversalModalityChat, Group: UniversalSelectedGroup{ID: anthropic.ID, Name: anthropic.Name, Platform: PlatformAnthropic}},
			},
			SelectedGroup: UniversalSelectedGroup{
				ID: anthropic.ID, Name: anthropic.Name, Platform: PlatformAnthropic,
			},
		},
		{
			ID:         "gpt-chat",
			Protocols:  []UniversalProtocol{UniversalProtocolOpenAI, UniversalProtocolCodex},
			Modalities: []UniversalModality{UniversalModalityChat},
			Routes: []UniversalCapabilityRoute{
				{Protocol: UniversalProtocolOpenAI, Modality: UniversalModalityChat, Group: UniversalSelectedGroup{ID: subscription.ID, Name: subscription.Name, Platform: PlatformOpenAI}},
				{Protocol: UniversalProtocolCodex, Modality: UniversalModalityChat, Group: UniversalSelectedGroup{ID: subscription.ID, Name: subscription.Name, Platform: PlatformOpenAI}},
			},
			SelectedGroup: UniversalSelectedGroup{
				ID: subscription.ID, Name: subscription.Name, Platform: PlatformOpenAI,
			},
		},
		{
			ID:         "veo-video",
			Protocols:  []UniversalProtocol{UniversalProtocolOpenAI},
			Modalities: []UniversalModality{UniversalModalityVideo},
			Routes: []UniversalCapabilityRoute{
				{Protocol: UniversalProtocolOpenAI, Modality: UniversalModalityVideo, Group: UniversalSelectedGroup{ID: subscription.ID, Name: subscription.Name, Platform: PlatformOpenAI}},
			},
			SelectedGroup: UniversalSelectedGroup{
				ID: subscription.ID, Name: subscription.Name, Platform: PlatformOpenAI,
			},
		},
	}, got)
}

func TestUS046_CapabilitiesRejectUnsupportedUnhintedModel(t *testing.T) {
	group := us046ActiveGroup(10, PlatformOpenAI, false)
	svc := newUniversalCapabilityService(
		&us046EntitlementStub{groups: []Group{group}},
		func(context.Context, int64, string) ([]string, bool, error) {
			return []string{"custom-unsupported-model"}, false, nil
		},
		func(context.Context, int64, string, string, UniversalShape) (bool, error) {
			return false, nil
		},
		nil,
	)

	models, err := svc.List(
		context.Background(),
		&APIKey{UserID: 9, RoutingMode: RoutingModeUniversal},
		UniversalProtocolOpenAI,
	)
	require.NoError(t, err)
	require.Empty(t, models)
}

func TestUS046_CapabilitiesPropagateEntitlementFailure(t *testing.T) {
	wantErr := errors.New("entitlements database unavailable")
	svc := newUniversalCapabilityService(
		&us046EntitlementStub{err: wantErr},
		func(context.Context, int64, string) ([]string, bool, error) {
			t.Fatal("candidate provider must not run after entitlement failure")
			return nil, false, nil
		},
		nil,
		nil,
	)

	_, err := svc.List(context.Background(), &APIKey{UserID: 9, RoutingMode: RoutingModeUniversal}, UniversalProtocolOpenAI)
	require.ErrorIs(t, err, wantErr)
}

func TestUS046_CapabilitiesPropagateProviderFailureInsteadOfReturningEmpty(t *testing.T) {
	group := us046ActiveGroup(10, PlatformOpenAI, false)
	wantErr := errors.New("account repository unavailable")
	svc := newUniversalCapabilityService(
		&us046EntitlementStub{groups: []Group{group}},
		func(context.Context, int64, string) ([]string, bool, error) {
			return nil, false, wantErr
		},
		nil,
		nil,
	)

	_, err := svc.List(context.Background(), &APIKey{UserID: 9, RoutingMode: RoutingModeUniversal}, UniversalProtocolOpenAI)
	require.ErrorIs(t, err, wantErr)
}

func TestUS046_ForcedPlatformDiscoveryIgnoresUnrelatedProviderFailure(t *testing.T) {
	openAIGroup := us046ActiveGroup(10, PlatformOpenAI, false)
	anthropicGroup := us046ActiveGroup(20, PlatformAnthropic, false)
	wantErr := errors.New("unrelated provider unavailable")
	var candidateGroups []int64
	svc := newUniversalCapabilityService(
		&us046EntitlementStub{groups: []Group{anthropicGroup, openAIGroup}},
		func(_ context.Context, groupID int64, _ string) ([]string, bool, error) {
			candidateGroups = append(candidateGroups, groupID)
			if groupID == anthropicGroup.ID {
				return nil, false, wantErr
			}
			return []string{"gpt-codex"}, false, nil
		},
		func(_ context.Context, groupID int64, _ string, model string, shape UniversalShape) (bool, error) {
			return groupID == openAIGroup.ID && model == "gpt-codex" && shape == ShapeOpenAIChat, nil
		},
		nil,
	)

	models, err := svc.List(
		context.Background(),
		&APIKey{UserID: 9, RoutingMode: RoutingModeUniversal},
		UniversalProtocolCodex,
	)
	require.NoError(t, err)
	require.Equal(t, []int64{openAIGroup.ID}, candidateGroups)
	require.Len(t, models, 1)
	require.Equal(t, openAIGroup.ID, models[0].SelectedGroup.ID)
}

func TestUS046_ForcedPlatformDiscoveryPropagatesRelevantProviderFailure(t *testing.T) {
	openAIGroup := us046ActiveGroup(10, PlatformOpenAI, false)
	wantErr := errors.New("openai provider unavailable")
	svc := newUniversalCapabilityService(
		&us046EntitlementStub{groups: []Group{openAIGroup}},
		func(context.Context, int64, string) ([]string, bool, error) {
			return nil, false, wantErr
		},
		nil,
		nil,
	)

	_, err := svc.List(
		context.Background(),
		&APIKey{UserID: 9, RoutingMode: RoutingModeUniversal},
		UniversalProtocolCodex,
	)
	require.ErrorIs(t, err, wantErr)
}

func TestUS046_CapabilitiesUnionMappedAndPassthroughCandidates(t *testing.T) {
	group := us046ActiveGroup(10, PlatformOpenAI, false)
	svc := newUniversalCapabilityService(
		&us046EntitlementStub{groups: []Group{group}},
		func(context.Context, int64, string) ([]string, bool, error) {
			return []string{"mapped-model"}, true, nil
		},
		func(_ context.Context, _ int64, _ string, model string, shape UniversalShape) (bool, error) {
			return (model == "mapped-model" || model == "passthrough-model") && shape == ShapeOpenAIChat, nil
		},
		func(context.Context, string) ([]string, error) {
			return []string{"passthrough-model"}, nil
		},
	)

	models, err := svc.List(context.Background(), &APIKey{UserID: 9, RoutingMode: RoutingModeUniversal}, UniversalProtocolOpenAI)
	require.NoError(t, err)
	modelIDs := make([]string, 0, len(models))
	for _, model := range models {
		modelIDs = append(modelIDs, model.ID)
	}
	require.Equal(t, []string{"mapped-model", "passthrough-model"}, modelIDs)
}

func TestUS046_CapabilitiesPropagateAvailabilityFailure(t *testing.T) {
	group := us046ActiveGroup(10, PlatformOpenAI, false)
	repo := &us046AccountRepoStub{accounts: []Account{{
		ID:       1,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-4o": "gpt-4o"},
		},
	}}}
	pricing := NewPricingCatalogService(nil)
	pricing.SetSourceForTesting(func() ([]byte, time.Time, bool) {
		return []byte(`{"gpt-4o":{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002,"litellm_provider":"openai"}}`), time.Unix(1, 0), true
	})
	wantErr := errors.New("availability database unavailable")
	filter := NewModelListFilter(pricing, NewPricingAvailabilityService(&us046AvailabilityRepoStub{err: wantErr}, time.Now))
	svc := NewUniversalCapabilityService(&APIKeyService{}, &GatewayService{accountRepo: repo}, filter)

	_, err := svc.List(context.Background(), &APIKey{
		UserID:      9,
		RoutingMode: RoutingModeDirect,
		GroupID:     &group.ID,
		Group:       &group,
	}, UniversalProtocolOpenAI)
	require.ErrorIs(t, err, wantErr)
}

func TestUS046_DirectKeyCapabilitiesStayGroupBound(t *testing.T) {
	group := us046ActiveGroup(10, PlatformOpenAI, false)
	svc := newUniversalCapabilityService(
		&us046EntitlementStub{err: errors.New("direct key must not load user entitlements")},
		func(_ context.Context, groupID int64, _ string) ([]string, bool, error) {
			require.Equal(t, group.ID, groupID)
			return []string{"gpt-direct"}, false, nil
		},
		func(_ context.Context, groupID int64, _ string, model string, shape UniversalShape) (bool, error) {
			return groupID == group.ID && model == "gpt-direct" && shape == ShapeOpenAIChat, nil
		},
		nil,
	)

	models, err := svc.List(context.Background(), &APIKey{
		UserID:      9,
		RoutingMode: RoutingModeDirect,
		GroupID:     &group.ID,
		Group:       &group,
	}, UniversalProtocolOpenAI)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, group.ID, models[0].SelectedGroup.ID)
	require.Equal(t, []UniversalProtocol{UniversalProtocolOpenAI}, models[0].Protocols)
}

func TestUS046_DirectKeyCapabilitiesRespectProtocolPlatform(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		protocol UniversalProtocol
		shape    UniversalShape
		model    string
	}{
		{
			name:     "codex requires an OpenAI group",
			platform: PlatformNewAPI,
			protocol: UniversalProtocolCodex,
			shape:    ShapeOpenAIChat,
			model:    "gpt-direct",
		},
		{
			name:     "Gemini protocol requires a Gemini-compatible group",
			platform: PlatformAnthropic,
			protocol: UniversalProtocolGemini,
			shape:    ShapeGemini,
			model:    "claude-direct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := us046ActiveGroup(10, tt.platform, false)
			svc := newUniversalCapabilityService(
				&us046EntitlementStub{err: errors.New("direct key must not load user entitlements")},
				func(context.Context, int64, string) ([]string, bool, error) {
					return []string{tt.model}, false, nil
				},
				func(_ context.Context, _ int64, _ string, model string, shape UniversalShape) (bool, error) {
					return model == tt.model && shape == tt.shape, nil
				},
				nil,
			)

			models, err := svc.List(context.Background(), &APIKey{
				UserID:      9,
				RoutingMode: RoutingModeDirect,
				GroupID:     &group.ID,
				Group:       &group,
			}, tt.protocol)
			require.NoError(t, err)
			require.Empty(t, models)
		})
	}
}

func TestUS046_CapabilityRequestReadsEachGroupAccountSnapshotOnce(t *testing.T) {
	group := us046ActiveGroup(10, PlatformOpenAI, false)
	repo := &us046AccountRepoStub{accounts: []Account{{
		ID:       1,
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"gpt-a": "gpt-a",
				"gpt-b": "gpt-b",
			},
		},
	}}}
	svc := NewUniversalCapabilityService(&APIKeyService{}, &GatewayService{accountRepo: repo}, nil)

	models, err := svc.List(context.Background(), &APIKey{
		UserID:      9,
		RoutingMode: RoutingModeDirect,
		GroupID:     &group.ID,
		Group:       &group,
	}, UniversalProtocolOpenAI)
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.Equal(t, int64(1), repo.groupCalls.Load(), "candidate inventory should read the group once")
	require.Equal(t, int64(1), repo.platformCalls.Load(), "shape checks should reuse one request-local account snapshot")
}

func TestUS046_DiscoveryCandidatesDistinguishPassthroughEmptyPoolAndFailure(t *testing.T) {
	const groupID int64 = 10
	t.Run("native account with empty mapping is passthrough", func(t *testing.T) {
		repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{
			groupID: {{ID: 1, Platform: PlatformAnthropic}},
		}}
		svc := &GatewayService{accountRepo: repo}

		ids, passthrough, err := svc.GetAvailableModelsForDiscovery(context.Background(), groupID, PlatformAnthropic)
		require.NoError(t, err)
		require.Empty(t, ids)
		require.True(t, passthrough)
	})

	t.Run("empty pool is not passthrough", func(t *testing.T) {
		svc := &GatewayService{accountRepo: &modelsListAccountRepoStub{byGroup: map[int64][]Account{}}}

		ids, passthrough, err := svc.GetAvailableModelsForDiscovery(context.Background(), groupID, PlatformAnthropic)
		require.NoError(t, err)
		require.Empty(t, ids)
		require.False(t, passthrough)
	})

	t.Run("newapi empty mapping is configuration absence", func(t *testing.T) {
		repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{
			groupID: {{ID: 1, Platform: PlatformNewAPI}},
		}}
		svc := &GatewayService{accountRepo: repo}

		ids, passthrough, err := svc.GetAvailableModelsForDiscovery(context.Background(), groupID, PlatformNewAPI)
		require.NoError(t, err)
		require.Empty(t, ids)
		require.False(t, passthrough)
	})

	t.Run("mixed mapped and native passthrough accounts preserve both sources", func(t *testing.T) {
		repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{
			groupID: {
				{ID: 1, Platform: PlatformOpenAI},
				{ID: 2, Platform: PlatformOpenAI, Credentials: map[string]any{
					"model_mapping": map[string]any{"mapped-model": "upstream-model"},
				}},
			},
		}}
		svc := &GatewayService{accountRepo: repo}

		ids, passthrough, err := svc.GetAvailableModelsForDiscovery(context.Background(), groupID, PlatformOpenAI)
		require.NoError(t, err)
		require.Equal(t, []string{"mapped-model"}, ids)
		require.True(t, passthrough)
	})

	t.Run("mixed scheduling accounts contribute candidates", func(t *testing.T) {
		repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{
			groupID: {{
				ID:       1,
				Platform: PlatformAntigravity,
				Extra:    map[string]any{"mixed_scheduling": true},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"claude-mixed": "claude-mixed"},
				},
			}},
		}}
		svc := &GatewayService{accountRepo: repo}

		ids, catalogFallback, err := svc.GetAvailableModelsForDiscovery(context.Background(), groupID, PlatformAnthropic)
		require.NoError(t, err)
		require.Equal(t, []string{"claude-mixed"}, ids)
		require.False(t, catalogFallback)
	})

	t.Run("wildcard mappings expand from the client-facing catalog", func(t *testing.T) {
		repo := &modelsListAccountRepoStub{byGroup: map[int64][]Account{
			groupID: {{
				ID:       1,
				Platform: PlatformNewAPI,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"qwen-*": "upstream-model"},
				},
			}},
		}}
		svc := &GatewayService{accountRepo: repo}

		ids, catalogFallback, err := svc.GetAvailableModelsForDiscovery(context.Background(), groupID, PlatformNewAPI)
		require.NoError(t, err)
		require.Empty(t, ids, "wildcard patterns are not client-callable model IDs")
		require.True(t, catalogFallback)
	})

	t.Run("repository failure is explicit", func(t *testing.T) {
		wantErr := errors.New("db unavailable")
		svc := &GatewayService{accountRepo: &modelsListAccountRepoStub{err: wantErr}}

		_, _, err := svc.GetAvailableModelsForDiscovery(context.Background(), groupID, PlatformOpenAI)
		require.ErrorIs(t, err, wantErr)
	})
}
