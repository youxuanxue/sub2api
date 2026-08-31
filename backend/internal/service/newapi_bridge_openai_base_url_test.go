package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/stretchr/testify/require"
)

// TestOpenAIChannelBaseURL_SchedulerCacheMatchesFreshReload pins the vstecs
// gateway 503 shape: scheduler Extra drops supplier_source_id, so plan-time and
// execution-time bridge BaseURL normalization must not depend on that marker.
func TestOpenAIChannelBaseURL_SchedulerCacheMatchesFreshReload(t *testing.T) {
	base := "https://token.vstecscloud.com/v1"
	model := "MiniMax-M2.7"
	creds := map[string]any{
		"base_url": base,
		"api_key":  "sk-test",
		"api_base_urls": map[string]any{
			APIProtocolChatCompletions: base,
		},
		ProtocolEndpointsExclusiveCredentialKey: true,
		"model_mapping": map[string]any{
			"MiniMax-M2.7": "MiniMax-M2.7",
		},
	}

	sched := &Account{
		ID:          101,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: creds,
		Extra:       map[string]any{}, // mirrors scheduler cache filter
	}
	fresh := &Account{
		ID:          101,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: creds,
		Extra:       map[string]any{SupplierSourceIDExtraKey: int64(6)},
	}

	require.False(t, IsSupplierManagedAccount(sched))
	require.True(t, IsSupplierManagedAccount(fresh))
	require.True(t, accountDeclaresExclusiveProtocolEndpoints(sched),
		"exclusive credential must survive scheduler-shaped Extra filtering")

	schedIn := newAPIBridgeChannelInputForModel(sched, 0, "", model).WithoutModelMapping()
	freshIn := newAPIBridgeChannelInputForModel(fresh, 0, "", model).WithoutModelMapping()
	require.Equal(t, freshIn.BaseURL, schedIn.BaseURL,
		"scheduler-shaped exclusive account must normalize OpenAI base_url the same as a fresh managed reload")
	require.Equal(t, "https://token.vstecscloud.com", schedIn.BaseURL)

	schedEP, err := bridge.ResolveTextEndpoint(schedIn, newapitypes.RelayFormatOpenAI, model)
	require.NoError(t, err)
	freshEP, err := bridge.ResolveTextEndpoint(freshIn, newapitypes.RelayFormatOpenAI, model)
	require.NoError(t, err)
	require.Equal(t, freshEP, schedEP)
	require.Equal(t, "https://token.vstecscloud.com/v1/chat/completions", schedEP)
	require.NotContains(t, schedEP, "/v1/v1/")

	schedExact, err := protocolExactEndpoint(sched, protocolrouter.ProtocolChatCompletions, model)
	require.NoError(t, err)
	freshExact, err := protocolExactEndpoint(fresh, protocolrouter.ProtocolChatCompletions, model)
	require.NoError(t, err)
	require.Equal(t, freshExact, schedExact)
	require.Equal(t, schedEP, schedExact)
}
