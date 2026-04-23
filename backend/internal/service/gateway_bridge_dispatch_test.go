package service

import (
	"context"
	"testing"
)

type bridgeToggleSettingRepo struct {
	values map[string]string
}

func (r *bridgeToggleSettingRepo) Get(_ context.Context, _ string) (*Setting, error) { return nil, nil }
func (r *bridgeToggleSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}
func (r *bridgeToggleSettingRepo) Set(_ context.Context, _, _ string) error { return nil }
func (r *bridgeToggleSettingRepo) GetMultiple(_ context.Context, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *bridgeToggleSettingRepo) SetMultiple(_ context.Context, _ map[string]string) error {
	return nil
}
func (r *bridgeToggleSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *bridgeToggleSettingRepo) Delete(_ context.Context, _ string) error { return nil }

func TestShouldDispatchToNewAPIBridge(t *testing.T) {
	svc := &GatewayService{}

	tests := []struct {
		name     string
		account  *Account
		endpoint string
		want     bool
	}{
		{
			name:     "nil account",
			account:  nil,
			endpoint: BridgeEndpointChatCompletions,
			want:     false,
		},
		{
			name: "channel type zero",
			account: &Account{
				ChannelType: 0,
			},
			endpoint: BridgeEndpointChatCompletions,
			want:     false,
		},
		{
			name: "positive channel type known endpoint",
			account: &Account{
				ChannelType: 5,
			},
			endpoint: BridgeEndpointResponses,
			want:     true,
		},
		{
			name: "positive channel type unknown endpoint",
			account: &Account{
				ChannelType: 5,
			},
			endpoint: "unknown",
			want:     false,
		},
		{
			name: "positive channel type embeddings endpoint",
			account: &Account{
				ChannelType: 9,
			},
			endpoint: BridgeEndpointEmbeddings,
			want:     true,
		},
		{
			name: "positive channel type images endpoint",
			account: &Account{
				ChannelType: 9,
			},
			endpoint: BridgeEndpointImages,
			want:     true,
		},
		{
			// Video submit must dispatch to the bridge for any account
			// with channel_type > 0 because there is no native sub2api
			// path for async task generation. Regressing this to false
			// silently 5xx's the entire /v1/video/generations endpoint.
			name:     "positive channel type video submit endpoint",
			account:  &Account{ChannelType: 45},
			endpoint: BridgeEndpointVideoSubmit,
			want:     true,
		},
		{
			name:     "positive channel type video fetch endpoint",
			account:  &Account{ChannelType: 45},
			endpoint: BridgeEndpointVideoFetch,
			want:     true,
		},
		{
			// Defensive: video endpoints with channel_type=0 must NOT
			// dispatch — there's no adaptor and the bridge would panic
			// (or worse, route to a wrong adaptor if a future channel
			// type happens to map to 0).
			name:     "zero channel type video submit endpoint",
			account:  &Account{ChannelType: 0},
			endpoint: BridgeEndpointVideoSubmit,
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

func TestShouldDispatchToNewAPIBridge_RespectsKillSwitch(t *testing.T) {
	svc := &GatewayService{
		settingService: &SettingService{
			settingRepo: &bridgeToggleSettingRepo{
				values: map[string]string{
					SettingKeyNewAPIBridgeEnabled: "false",
				},
			},
		},
	}
	account := &Account{ChannelType: 7}
	if svc.ShouldDispatchToNewAPIBridge(account, BridgeEndpointResponses) {
		t.Fatalf("expected bridge dispatch disabled by setting")
	}
}
