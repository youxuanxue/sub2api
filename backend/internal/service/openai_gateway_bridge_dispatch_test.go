package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestEmbeddingsDispatch_NewAPINeverFallsBackToOpenAI(t *testing.T) {
	for _, scenario := range []string{"disabled_bridge", "missing_channel"} {
		t.Run(scenario, func(t *testing.T) {
			upstream := &httpUpstreamRecorder{}
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			account := &Account{ID: 110, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeAli, Credentials: map[string]any{"api_key": "ali-test-key", "base_url": "https://dashscope.aliyuncs.com"}}
			if scenario == "disabled_bridge" {
				svc.settingService = &SettingService{settingRepo: &bridgeToggleSettingRepo{values: map[string]string{SettingKeyNewAPIBridgeEnabled: "off"}}}
			} else {
				account.ChannelType = 0
			}
			result, err := svc.ForwardAsEmbeddingsDispatched(context.Background(), nil, account, []byte(`{"model":"text-embedding-v4","input":"hello"}`), "")
			require.ErrorContains(t, err, "embeddings adaptor unavailable")
			require.Nil(t, result)
			require.Nil(t, upstream.lastReq, "NewAPI credentials must never reach the native OpenAI transport")
		})
	}
}

func TestOpenAIShouldDispatchToNewAPIBridge(t *testing.T) {
	svc := &OpenAIGatewayService{}

	tests := []struct {
		name     string
		account  *Account
		endpoint string
		want     bool
	}{
		{
			name:     "nil account",
			account:  nil,
			endpoint: BridgeEndpointResponses,
			want:     false,
		},
		{
			name: "channel type zero",
			account: &Account{
				ChannelType: 0,
			},
			endpoint: BridgeEndpointResponses,
			want:     false,
		},
		{
			name: "positive channel type responses endpoint",
			account: &Account{
				ChannelType: 3,
				Platform:    domain.PlatformOpenAI,
			},
			endpoint: BridgeEndpointResponses,
			want:     true,
		},
		{
			name: "positive channel type chat endpoint",
			account: &Account{
				ChannelType: 2,
				Platform:    domain.PlatformOpenAI,
			},
			endpoint: BridgeEndpointChatCompletions,
			want:     true,
		},
		{
			name: "deepseek newapi chat endpoint",
			account: &Account{
				Type:        AccountTypeAPIKey,
				ChannelType: newapiconstant.ChannelTypeDeepSeek,
				Platform:    domain.PlatformNewAPI,
			},
			endpoint: BridgeEndpointChatCompletions,
			want:     true,
		},
		{
			name: "volcengine Agent Plan uses native OpenAI path",
			account: &Account{
				Type:        AccountTypeAPIKey,
				ChannelType: newapiconstant.ChannelTypeVolcEngine,
				Platform:    domain.PlatformNewAPI,
				Credentials: map[string]any{"base_url": newapiintegration.VolcEngineAgentPlanBaseURL},
			},
			endpoint: BridgeEndpointResponses,
			want:     false,
		},
		{
			name: "volcengine Agent Plan chat uses native OpenAI path",
			account: &Account{
				Type:        AccountTypeAPIKey,
				ChannelType: newapiconstant.ChannelTypeVolcEngine,
				Platform:    domain.PlatformNewAPI,
				Credentials: map[string]any{"base_url": newapiintegration.VolcEngineAgentPlanBaseURL},
			},
			endpoint: BridgeEndpointChatCompletions,
			want:     false,
		},
		{
			name: "qianfan Token Plan chat uses native OpenAI path",
			account: &Account{
				Type:        AccountTypeAPIKey,
				ChannelType: newapiconstant.ChannelTypeBaiduV2,
				Platform:    domain.PlatformNewAPI,
				Credentials: map[string]any{"base_url": newapiintegration.QianfanTokenPlanBaseURL},
			},
			endpoint: BridgeEndpointChatCompletions,
			want:     false,
		},
		{
			name: "qianfan payg still uses newapi bridge",
			account: &Account{
				Type:        AccountTypeAPIKey,
				ChannelType: newapiconstant.ChannelTypeBaiduV2,
				Platform:    domain.PlatformNewAPI,
				Credentials: map[string]any{"base_url": newapiintegration.QianfanBaseURL},
			},
			endpoint: BridgeEndpointChatCompletions,
			want:     true,
		},
		{
			name: "positive channel type unknown endpoint",
			account: &Account{
				ChannelType: 2,
				Platform:    domain.PlatformOpenAI,
			},
			endpoint: "unknown",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svc.ShouldDispatchToNewAPIBridge(tt.account, tt.endpoint)
			if got != tt.want {
				t.Fatalf("ShouldDispatchToNewAPIBridge() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOpenAIShouldDispatchToNewAPIBridge_RespectsKillSwitch(t *testing.T) {
	svc := &OpenAIGatewayService{
		settingService: &SettingService{
			settingRepo: &bridgeToggleSettingRepo{
				values: map[string]string{
					SettingKeyNewAPIBridgeEnabled: "off",
				},
			},
		},
	}
	account := &Account{ChannelType: 3, Platform: domain.PlatformOpenAI}
	if svc.ShouldDispatchToNewAPIBridge(account, BridgeEndpointChatCompletions) {
		t.Fatalf("expected bridge dispatch disabled by setting")
	}
}
