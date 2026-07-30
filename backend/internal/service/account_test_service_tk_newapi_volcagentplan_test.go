//go:build unit

package service

import (
	"context"
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
		ID:          88,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"base_url": "https://ark.cn-beijing.volces.com/api/plan/v3",
		},
	}
	got := NewAPIAvailableModelPresetIDs(account)
	require.NotEmpty(t, got)
	require.Equal(t, tkServedModelsManifestPresetIDsForAccount("88"), got)
	require.Contains(t, got, newapiintegration.VolcEngineAgentPlanDefaultTestModel)
	display := NewAPIModelDisplayIDsForAccount(account)
	require.ElementsMatch(t, got, display, "all verified Agent Plan models are displayable")
	require.Contains(t, display, "minimax-m3")
}

func TestNativeAgentPlanUsesNewAPIKeyCredential(t *testing.T) {
	t.Parallel()
	account := &Account{
		ID:          88,
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

	fallbackKey, targetURL, err := svc.resolveCCFallbackTarget(account)
	require.NoError(t, err)
	require.Equal(t, "agent-plan-key", fallbackKey)
	require.Equal(t, newapiintegration.VolcEngineAgentPlanBaseURL+"/chat/completions", targetURL)
}
