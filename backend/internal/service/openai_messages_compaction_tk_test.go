//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestResolveOpenAICompatMessagesCompactionPolicy_AccountOverridesGroup(t *testing.T) {
	groupEnabled := true
	groupThreshold := 200000
	group := &Group{
		MessagesCompactionEnabled:              &groupEnabled,
		MessagesCompactionInputTokensThreshold: &groupThreshold,
	}
	account := &Account{Extra: map[string]any{
		"messages_compaction_enabled":                true,
		"messages_compaction_input_tokens_threshold": 150000,
	}}

	policy := resolveOpenAICompatMessagesCompactionPolicy(account, group)
	require.True(t, policy.enabled)
	require.Equal(t, 150000, policy.inputTokenLimit)
}

func TestResolveOpenAICompatMessagesCompactionPolicy_AccountDisableWins(t *testing.T) {
	groupEnabled := true
	groupThreshold := 200000
	group := &Group{
		MessagesCompactionEnabled:              &groupEnabled,
		MessagesCompactionInputTokensThreshold: &groupThreshold,
	}
	account := &Account{Extra: map[string]any{
		"messages_compaction_enabled": false,
	}}

	policy := resolveOpenAICompatMessagesCompactionPolicy(account, group)
	require.False(t, policy.enabled)
	require.Zero(t, policy.inputTokenLimit)
}

func TestResolveOpenAICompatMessagesCompactionPolicy_GroupOnly(t *testing.T) {
	groupEnabled := true
	groupThreshold := 220000
	group := &Group{
		MessagesCompactionEnabled:              &groupEnabled,
		MessagesCompactionInputTokensThreshold: &groupThreshold,
	}

	policy := resolveOpenAICompatMessagesCompactionPolicy(&Account{}, group)
	require.True(t, policy.enabled)
	require.Equal(t, 220000, policy.inputTokenLimit)
}

func TestResolveOpenAICompatMessagesCompactionPolicy_UnconfiguredDisabled(t *testing.T) {
	policy := resolveOpenAICompatMessagesCompactionPolicy(&Account{}, nil)
	require.False(t, policy.enabled)
	require.Zero(t, policy.inputTokenLimit)
}

func TestShouldApplyOpenAICompatMessagesCompaction(t *testing.T) {
	policy := openAICompatMessagesCompactionPolicy{enabled: true, inputTokenLimit: 10}
	req := &apicompat.AnthropicRequest{
		Messages: []apicompat.AnthropicMessage{
			{Role: "user", Content: []byte(`"hello world hello world hello world hello world"`)},
		},
	}
	require.True(t, shouldApplyOpenAICompatMessagesCompaction(policy, req))
	require.False(t, shouldApplyOpenAICompatMessagesCompaction(openAICompatMessagesCompactionPolicy{}, req))
}
