//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveMessagesDispatchModel_NewAPIGroupUsesRegistryNotGPTDefaults(t *testing.T) {
	g := &Group{
		Name:     "Google-Vertex",
		Platform: PlatformNewAPI,
	}
	require.Equal(t, "gemini-2.5-pro", g.ResolveMessagesDispatchModel("claude-opus-4-6"))
	require.Equal(t, "gemini-3.6-flash", g.ResolveMessagesDispatchModel("claude-sonnet-4-6"))
	require.Equal(t, "gemini-3.5-flash-lite", g.ResolveMessagesDispatchModel("claude-haiku-4-5"))
}

func TestResolveMessagesDispatchModel_UnknownNewAPIGroupDoesNotFallbackToGPT(t *testing.T) {
	g := &Group{
		Name:     "unknown-vendor-group",
		Platform: PlatformNewAPI,
	}
	require.Equal(t, "", g.ResolveMessagesDispatchModel("claude-opus-4-6"))
}

func TestValidateGroupMessagesDispatchModelConfig_RejectsGPTOnGeminiGroup(t *testing.T) {
	err := validateGroupMessagesDispatchModelConfig(&Group{
		Name:                  "Google-Vertex",
		Platform:              PlatformNewAPI,
		AllowMessagesDispatch: true,
		MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:   "gpt-5.6-sol",
			SonnetMappedModel: "gemini-2.5-flash",
			HaikuMappedModel:  "gemini-2.5-flash-lite",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "opus_mapped_model")
}

func TestValidateGroupMessagesDispatchModelConfig_AcceptsRegistryGeminiMapping(t *testing.T) {
	err := validateGroupMessagesDispatchModelConfig(&Group{
		Name:                  "Google-Vertex",
		Platform:              PlatformNewAPI,
		AllowMessagesDispatch: true,
		MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:   "gemini-2.5-pro",
			SonnetMappedModel: "gemini-3.6-flash",
			HaikuMappedModel:  "gemini-3.5-flash-lite",
		},
	})
	require.NoError(t, err)
}

func TestValidateGroupMessagesDispatchModelConfig_UnknownGroupRequiresRegistryEntry(t *testing.T) {
	err := validateGroupMessagesDispatchModelConfig(&Group{
		Name:                  "brand-new-vendor",
		Platform:              PlatformNewAPI,
		AllowMessagesDispatch: true,
		MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:   "glm-5.2",
			SonnetMappedModel: "glm-4.7",
			HaikuMappedModel:  "glm-4.5-air",
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "tk_messages_dispatch_family_registry.json")
}

func TestResolveMessagesDispatchModel_GeminiPlatformUsesPlatformDefaults(t *testing.T) {
	g := &Group{
		Name:     "custom-gemini-pool",
		Platform: PlatformGemini,
	}
	require.Equal(t, "gemini-2.5-pro", g.ResolveMessagesDispatchModel("claude-opus-4-6"))
	require.Equal(t, "gemini-3.6-flash", g.ResolveMessagesDispatchModel("claude-sonnet-4-6"))
	require.Equal(t, "gemini-3.5-flash-lite", g.ResolveMessagesDispatchModel("claude-haiku-4-5"))
}

func TestValidateGroupMessagesDispatchModelConfig_GeminiPlatformImplicitFamily(t *testing.T) {
	err := validateGroupMessagesDispatchModelConfig(&Group{
		Name:                  "custom-gemini-pool",
		Platform:              PlatformGemini,
		AllowMessagesDispatch: true,
		MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{
			OpusMappedModel:   "gemini-2.5-pro",
			SonnetMappedModel: "gemini-3.6-flash",
			HaikuMappedModel:  "gemini-3.5-flash-lite",
		},
	})
	require.NoError(t, err)
}
