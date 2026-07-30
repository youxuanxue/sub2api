//go:build unit

package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/volcengine"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestVolcEngineAdaptor_GetRequestURL_AgentPlanChatCompletions(t *testing.T) {
	t.Parallel()
	adaptor := volcengine.Adaptor{}
	url, err := adaptor.GetRequestURL(&relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl: newapiintegration.VolcEngineAgentPlanBaseKey,
		},
		RelayMode: relayconstant.RelayModeChatCompletions,
	})
	require.NoError(t, err)
	require.Equal(t, "https://ark.cn-beijing.volces.com/api/plan/v3/chat/completions", url)
}

func TestDefaultNewAPIAccountTestModel_AgentPlanUsesArkCodeLatest(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Credentials: map[string]any{
			"base_url": newapiintegration.VolcEngineAgentPlanBaseKey,
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
