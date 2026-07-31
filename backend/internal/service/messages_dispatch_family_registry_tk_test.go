//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func requireTkMessagesDispatchGroupDefaults(t *testing.T, groupName string) OpenAIMessagesDispatchModelConfig {
	t.Helper()
	cfg, ok := TkMessagesDispatchGroupDefaults(groupName)
	require.Truef(t, ok, "group %q must exist in tk_messages_dispatch_family_registry.json", groupName)
	return cfg
}

func requireTkMessagesDispatchPlatformDefaults(t *testing.T, platform string) OpenAIMessagesDispatchModelConfig {
	t.Helper()
	cfg, ok := TkMessagesDispatchPlatformDefaults(platform)
	require.Truef(t, ok, "platform %q must exist in tk_messages_dispatch_family_registry.json platform_defaults", platform)
	return cfg
}

func TestTkMessagesDispatchRegistryMatchesRuntimeOpenAIGrokConstants(t *testing.T) {
	openai, ok := TkMessagesDispatchPlatformDefaults(PlatformOpenAI)
	require.True(t, ok)
	require.Equal(t, defaultOpenAIMessagesDispatchOpusMappedModel, openai.OpusMappedModel)
	require.Equal(t, defaultOpenAIMessagesDispatchSonnetMappedModel, openai.SonnetMappedModel)
	require.Equal(t, defaultOpenAIMessagesDispatchHaikuMappedModel, openai.HaikuMappedModel)

	grok, ok := TkMessagesDispatchPlatformDefaults(PlatformGrok)
	require.True(t, ok)
	require.Equal(t, defaultGrokMessagesDispatchOpusMappedModel, grok.OpusMappedModel)
	require.Equal(t, defaultGrokMessagesDispatchSonnetMappedModel, grok.SonnetMappedModel)
	require.Equal(t, defaultGrokMessagesDispatchHaikuMappedModel, grok.HaikuMappedModel)
}

func TestResolveMessagesDispatchModel_NewAPIGroupUsesRegistryNotGPTDefaults(t *testing.T) {
	vertex := requireTkMessagesDispatchGroupDefaults(t, "Google-Vertex")
	g := &Group{
		Name:     "Google-Vertex",
		Platform: PlatformNewAPI,
	}
	require.Equal(t, vertex.OpusMappedModel, g.ResolveMessagesDispatchModel("claude-opus-4-6"))
	require.Equal(t, vertex.SonnetMappedModel, g.ResolveMessagesDispatchModel("claude-sonnet-4-6"))
	require.Equal(t, vertex.HaikuMappedModel, g.ResolveMessagesDispatchModel("claude-haiku-4-5"))
}

func TestResolveMessagesDispatchModel_UnknownNewAPIGroupDoesNotFallbackToGPT(t *testing.T) {
	g := &Group{
		Name:     "unknown-vendor-group",
		Platform: PlatformNewAPI,
	}
	require.Equal(t, "", g.ResolveMessagesDispatchModel("claude-opus-4-6"))
}

func TestValidateGroupMessagesDispatchModelConfig_RejectsGPTOnGeminiGroup(t *testing.T) {
	vertex := requireTkMessagesDispatchGroupDefaults(t, "Google-Vertex")
	wrongOpus := TkMessagesDispatchCrossFamilySample("gemini")
	require.NotEmpty(t, wrongOpus, "boundary sample: cross-family model for gemini rejection")

	err := validateGroupMessagesDispatchModelConfig(&Group{
		Name:                  "Google-Vertex",
		Platform:              PlatformNewAPI,
		AllowMessagesDispatch: true,
		MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:   wrongOpus,
			SonnetMappedModel: vertex.SonnetMappedModel,
			HaikuMappedModel:  vertex.HaikuMappedModel,
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "opus_mapped_model")
}

func TestValidateGroupMessagesDispatchModelConfig_AcceptsRegistryGeminiMapping(t *testing.T) {
	vertex := requireTkMessagesDispatchGroupDefaults(t, "Google-Vertex")
	err := validateGroupMessagesDispatchModelConfig(&Group{
		Name:                        "Google-Vertex",
		Platform:                    PlatformNewAPI,
		AllowMessagesDispatch:       true,
		MessagesDispatchModelConfig: vertex,
	})
	require.NoError(t, err)
}

func TestValidateGroupMessagesDispatchModelConfig_UnknownGroupRequiresRegistryEntry(t *testing.T) {
	glm := requireTkMessagesDispatchGroupDefaults(t, "glm")
	err := validateGroupMessagesDispatchModelConfig(&Group{
		Name:                        "brand-new-vendor",
		Platform:                    PlatformNewAPI,
		AllowMessagesDispatch:       true,
		MessagesDispatchModelConfig: glm,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "tk_messages_dispatch_family_registry.json")
}

func TestResolveMessagesDispatchModel_GeminiPlatformUsesPlatformDefaults(t *testing.T) {
	gemini := requireTkMessagesDispatchPlatformDefaults(t, PlatformGemini)
	g := &Group{
		Name:     "custom-gemini-pool",
		Platform: PlatformGemini,
	}
	require.Equal(t, gemini.OpusMappedModel, g.ResolveMessagesDispatchModel("claude-opus-4-6"))
	require.Equal(t, gemini.SonnetMappedModel, g.ResolveMessagesDispatchModel("claude-sonnet-4-6"))
	require.Equal(t, gemini.HaikuMappedModel, g.ResolveMessagesDispatchModel("claude-haiku-4-5"))
}

func TestValidateGroupMessagesDispatchModelConfig_GeminiPlatformImplicitFamily(t *testing.T) {
	gemini := requireTkMessagesDispatchPlatformDefaults(t, PlatformGemini)
	err := validateGroupMessagesDispatchModelConfig(&Group{
		Name:                        "custom-gemini-pool",
		Platform:                    PlatformGemini,
		AllowMessagesDispatch:       true,
		MessagesDispatchModelConfig: gemini,
	})
	require.NoError(t, err)
}
