package service

import "testing"

func TestAccount_IsAnthropicOAuthPassthroughEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{
			name: "oauth enabled",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"anthropic_oauth_passthrough": true},
			},
			want: true,
		},
		{
			name: "setup token enabled",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeSetupToken,
				Extra:    map[string]any{"anthropic_oauth_passthrough": true},
			},
			want: true,
		},
		{
			name: "oauth disabled",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"anthropic_oauth_passthrough": false},
			},
			want: false,
		},
		{
			name: "api key ignored",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeAPIKey,
				Extra:    map[string]any{"anthropic_oauth_passthrough": true},
			},
			want: false,
		},
		{
			name: "wrong platform",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"anthropic_oauth_passthrough": true},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.account.IsAnthropicOAuthPassthroughEnabled(); got != tt.want {
				t.Fatalf("IsAnthropicOAuthPassthroughEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
