//go:build unit

package service

func antigravityAPIKeyFixture(id int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformAntigravity,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"base_url":  "https://upstream.example.test",
			"pool_mode": true,
			"model_mapping": map[string]any{
				"gemini-3-flash":    "gemini-3-flash-tiered",
				"claude-sonnet-4-5": "claude-sonnet-4-5",
				"claude-sonnet-4-6": "claude-sonnet-4-6",
			},
		},
	}
}
