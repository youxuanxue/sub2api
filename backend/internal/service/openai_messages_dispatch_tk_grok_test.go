//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveMessagesDispatchModel_GrokPlatformUsesGrokDefaults(t *testing.T) {
	t.Parallel()

	g := &Group{Platform: PlatformGrok}

	require.Equal(t, defaultGrokMessagesDispatchOpusMappedModel, g.ResolveMessagesDispatchModel("claude-opus-4-6"))
	require.Equal(t, defaultGrokMessagesDispatchSonnetMappedModel, g.ResolveMessagesDispatchModel("claude-sonnet-4-6"))
	require.Equal(t, defaultGrokMessagesDispatchHaikuMappedModel, g.ResolveMessagesDispatchModel("claude-haiku-4-5"))
}

func TestResolveMessagesDispatchModel_GrokPlatformPrefersConfiguredMapping(t *testing.T) {
	t.Parallel()

	g := &Group{
		Platform: PlatformGrok,
		MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:   "grok-custom-opus",
			SonnetMappedModel: "grok-custom-sonnet",
			HaikuMappedModel:  "grok-custom-haiku",
		},
	}

	require.Equal(t, "grok-custom-opus", g.ResolveMessagesDispatchModel("claude-opus-4-6"))
	require.Equal(t, "grok-custom-sonnet", g.ResolveMessagesDispatchModel("claude-sonnet-4-6"))
	require.Equal(t, "grok-custom-haiku", g.ResolveMessagesDispatchModel("claude-haiku-4-5"))
}

func TestResolveMessagesDispatchModel_OpenAIPlatformKeepsGPTDefaults(t *testing.T) {
	t.Parallel()

	g := &Group{Platform: PlatformOpenAI}

	require.Equal(t, defaultOpenAIMessagesDispatchOpusMappedModel, g.ResolveMessagesDispatchModel("claude-opus-4-6"))
	require.Equal(t, defaultOpenAIMessagesDispatchSonnetMappedModel, g.ResolveMessagesDispatchModel("claude-sonnet-4-6"))
	require.Equal(t, defaultOpenAIMessagesDispatchHaikuMappedModel, g.ResolveMessagesDispatchModel("claude-haiku-4-5"))
}

func TestTkGroupKeepsDispatchConfig_Grok(t *testing.T) {
	t.Parallel()

	g := newGroupWithDispatchConfig(PlatformGrok)
	sanitizeGroupMessagesDispatchFields(g)
	assertDispatchPreserved(t, g)
}
