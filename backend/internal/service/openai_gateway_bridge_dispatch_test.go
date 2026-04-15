package service

import "testing"

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
			},
			endpoint: BridgeEndpointResponses,
			want:     true,
		},
		{
			name: "positive channel type chat endpoint",
			account: &Account{
				ChannelType: 2,
			},
			endpoint: BridgeEndpointChatCompletions,
			want:     true,
		},
		{
			name: "positive channel type unknown endpoint",
			account: &Account{
				ChannelType: 2,
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
	account := &Account{ChannelType: 3}
	if svc.ShouldDispatchToNewAPIBridge(account, BridgeEndpointChatCompletions) {
		t.Fatalf("expected bridge dispatch disabled by setting")
	}
}
