//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpenAICompatContinuationEnabled(t *testing.T) {
	cases := []struct {
		name    string
		account *Account
		model   string
		want    bool
	}{
		// OAuth accounts on Codex/GPT-5 models: must be enabled now.
		{
			name:    "oauth gpt-5 enabled",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5",
			want:    true,
		},
		{
			name:    "oauth gpt-5.4 enabled",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-5.4",
			want:    true,
		},
		{
			name:    "oauth codex enabled",
			account: &Account{Type: AccountTypeOAuth},
			model:   "codex-pro-20250601",
			want:    true,
		},
		// OAuth on non-Codex/non-GPT-5 models: disabled.
		{
			name:    "oauth gpt-4o disabled",
			account: &Account{Type: AccountTypeOAuth},
			model:   "gpt-4o",
			want:    false,
		},
		{
			name:    "oauth claude model disabled",
			account: &Account{Type: AccountTypeOAuth},
			model:   "claude-opus-4-7",
			want:    false,
		},
		// APIKey accounts (regression — must still work).
		{
			name:    "apikey gpt-5 enabled",
			account: &Account{Type: AccountTypeAPIKey},
			model:   "gpt-5",
			want:    true,
		},
		{
			name:    "apikey non-codex disabled",
			account: &Account{Type: AccountTypeAPIKey},
			model:   "gpt-4o",
			want:    false,
		},
		// Edge cases.
		{
			name:    "nil account disabled",
			account: nil,
			model:   "gpt-5",
			want:    false,
		},
		{
			name:    "anthropic account disabled",
			account: &Account{Type: "anthropic"},
			model:   "gpt-5",
			want:    false,
		},
		{
			name:    "newapi account disabled",
			account: &Account{Type: "newapi"},
			model:   "gpt-5",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := openAICompatContinuationEnabled(tc.account, tc.model)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestOpenAICompatContinuationAllowedAccountType(t *testing.T) {
	assert.True(t, openAICompatContinuationAllowedAccountType(&Account{Type: AccountTypeAPIKey}))
	assert.True(t, openAICompatContinuationAllowedAccountType(&Account{Type: AccountTypeOAuth}))
	assert.False(t, openAICompatContinuationAllowedAccountType(nil))
	assert.False(t, openAICompatContinuationAllowedAccountType(&Account{Type: "anthropic"}))
	assert.False(t, openAICompatContinuationAllowedAccountType(&Account{Type: "newapi"}))
}

func TestOpenAICompatShouldTrimForContinuation(t *testing.T) {
	// Only APIKey accounts get input trimmed: OpenAI Platform retains full history.
	assert.True(t, openAICompatShouldTrimForContinuation(&Account{Type: AccountTypeAPIKey}))
	// OAuth must NOT trim: full replay keeps system-prompt in input[0] so the
	// codex transform can extract it into the instructions field.
	assert.False(t, openAICompatShouldTrimForContinuation(&Account{Type: AccountTypeOAuth}))
	assert.False(t, openAICompatShouldTrimForContinuation(nil))
	assert.False(t, openAICompatShouldTrimForContinuation(&Account{Type: "anthropic"}))
}
