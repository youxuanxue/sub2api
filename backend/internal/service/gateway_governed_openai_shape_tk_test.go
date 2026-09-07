//go:build unit

package service

import "testing"

func TestResolveGovernedOpenAIShapeMode(t *testing.T) {
	t.Parallel()

	agRelay := &Account{
		Platform: PlatformAntigravity,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api-us4.tokenkey.dev",
			"api_key":  "sk-edge-stub",
		},
	}
	agOAuth := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	openaiKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	cases := []struct {
		name    string
		account *Account
		model   string
		want    GovernedOpenAIShapeMode
	}{
		{
			name:    "antigravity edge relay gemini chat uses gemini compat hop",
			account: agRelay,
			model:   "gemini-3.8-flash",
			want:    GovernedOpenAIShapeGeminiCompat,
		},
		{
			name:    "antigravity edge relay claude chat uses gateway messages relay",
			account: agRelay,
			model:   "claude-sonnet-4-6",
			want:    GovernedOpenAIShapeAntigravityClaudeRelay,
		},
		{
			name:    "antigravity oauth gemini stays gemini compat predicate",
			account: agOAuth,
			model:   "gemini-3.8-flash",
			want:    GovernedOpenAIShapeGeminiCompat,
		},
		{
			name:    "antigravity oauth claude uses cloud code hop",
			account: agOAuth,
			model:   "claude-sonnet-4-6",
			want:    GovernedOpenAIShapeAntigravityOAuthCloudCode,
		},
		{
			name:    "openai api key stays openai gateway",
			account: openaiKey,
			model:   "gpt-5.6",
			want:    GovernedOpenAIShapeOpenAI,
		},
		{
			name:    "nil account stays openai gateway",
			account: nil,
			model:   "gemini-3.8-flash",
			want:    GovernedOpenAIShapeOpenAI,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveGovernedOpenAIShapeMode(tc.account, tc.model); got != tc.want {
				t.Fatalf("ResolveGovernedOpenAIShapeMode = %v, want %v", got, tc.want)
			}
		})
	}
}
