//go:build unit

package service

import (
	"context"
	"slices"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestVolcEngineAgentPlanBaseURLIsNative(t *testing.T) {
	t.Parallel()
	require.Equal(t, newapiintegration.VolcEngineAgentPlanBaseURL,
		newapiintegration.NormalizeArkChannelBaseURL(newapiconstant.ChannelTypeVolcEngine,
			newapiintegration.VolcEngineAgentPlanBaseKey))
}

func TestDefaultNewAPIAccountTestModel_AgentPlanUsesArkCodeLatest(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"base_url": newapiintegration.VolcEngineAgentPlanBaseURL,
		},
	}
	require.Equal(t, newapiintegration.VolcEngineAgentPlanDefaultTestModel, defaultNewAPIAccountTestModel(account))
}

func TestNewAPIAvailableModelPresetIDs_AgentPlan(t *testing.T) {
	t.Parallel()
	account := &Account{
		ID:          89,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"base_url": "https://ark.cn-beijing.volces.com/api/plan/v3",
		},
	}
	got := NewAPIAvailableModelPresetIDs(account)
	require.NotEmpty(t, got)
	require.Equal(t, tkServedModelsManifestPresetIDsForSelector(account.Platform, account.ChannelType, account.GetBaseURL()), got)
	require.Contains(t, got, newapiintegration.VolcEngineAgentPlanDefaultTestModel)
	display := NewAPIModelDisplayIDsForAccount(account)
	require.ElementsMatch(t, tkServedModelsManifestDisplayPresetIDsForSelector(
		account.Platform, account.ChannelType, account.GetBaseURL(),
	), display)
	require.Contains(t, got, "minimax-m2.7", "withdrawal must preserve API compatibility")
	require.NotContains(t, display, "minimax-m2.7", "withdrawn Agent Plan models must not be recommended")
	require.Contains(t, display, "minimax-m3")
}

func TestNewAPIModelMappingPresetIDs_AgentPlanUsesPropertiesNotAccountID(t *testing.T) {
	t.Parallel()

	makeAccount := func(id int64, baseURL string) *Account {
		return &Account{
			ID:          id,
			Platform:    PlatformNewAPI,
			Type:        AccountTypeAPIKey,
			ChannelType: newapiconstant.ChannelTypeVolcEngine,
			Credentials: map[string]any{"base_url": baseURL},
		}
	}
	agentPlan := makeAccount(88, newapiintegration.VolcEngineAgentPlanBaseURL)
	otherID := makeAccount(12345, newapiintegration.VolcEngineAgentPlanBaseURL)
	payAsYouGo := makeAccount(88, "https://ark.cn-beijing.volces.com/api/v3")

	require.Equal(t, NewAPIModelMappingPresetIDsForAccount(agentPlan), NewAPIModelMappingPresetIDsForAccount(otherID))
	require.NotEqual(t, NewAPIModelMappingPresetIDsForAccount(agentPlan), NewAPIModelMappingPresetIDsForAccount(payAsYouGo))
	require.NotContains(t, NewAPIModelMappingPresetIDsForAccount(payAsYouGo), "doubao-seed-2.0-pro")

	owner := loadTkServedModelsOwnerProjectionForTest(t)
	planIDs := NewAPIModelMappingPresetIDsForAccount(agentPlan)
	payAsYouGoIDs := NewAPIModelMappingPresetIDsForAccount(payAsYouGo)
	for _, modelID := range owner.IDsByScope["newapi:45:"+newapiintegration.VolcEngineAgentPlanBaseURL] {
		require.Contains(t, planIDs, modelID)
		if !slices.Contains(owner.IDsByChannel[45], modelID) {
			require.NotContains(t, payAsYouGoIDs, modelID, "plan-only models must not leak into pay-as-you-go presets")
		}
	}
}

func TestNativeAgentPlanUsesNewAPIKeyCredential(t *testing.T) {
	t.Parallel()
	account := &Account{
		ID:          89,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"api_key":  "agent-plan-key",
			"base_url": newapiintegration.VolcEngineAgentPlanBaseURL,
		},
	}

	svc := &OpenAIGatewayService{}
	token, kind, err := svc.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "agent-plan-key", token)
	require.Equal(t, "apikey", kind)

	fallbackKey, targetURL, err := svc.resolveCCFallbackTarget(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "agent-plan-key", fallbackKey)
	require.Equal(t, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", targetURL)
}
