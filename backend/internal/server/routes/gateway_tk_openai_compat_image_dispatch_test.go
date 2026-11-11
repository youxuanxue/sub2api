package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestTkOpenAIImageGenerationsDispatch_PlatformOpenAIUsesImages(t *testing.T) {
	require.Equal(t, "images", tkOpenAIImageGenerationsDispatch(service.PlatformOpenAI))
}

func TestTkOpenAIImageGenerationsDispatch_AntigravityUsesGeminiImages(t *testing.T) {
	require.Equal(t, "gemini_images", tkOpenAIImageGenerationsDispatch(service.PlatformAntigravity))
	require.Equal(t, "gemini_images", tkOpenAIImageGenerationsDispatch(service.PlatformGemini))
}

func TestTkOpenAIImageGenerationsDispatch_CompatPoolUsesImageGenerations(t *testing.T) {
	require.Equal(t, "image_generations", tkOpenAIImageGenerationsDispatch(service.PlatformNewAPI))
}

func TestTkOpenAIImageGenerationsDispatch_GrokUsesGrokImages(t *testing.T) {
	require.Equal(t, "grok_images", tkOpenAIImageGenerationsDispatch(service.PlatformGrok))
}

func TestTkOpenAIImageGenerationsDispatch_UnknownPlatformNotFound(t *testing.T) {
	require.Equal(t, "not_found", tkOpenAIImageGenerationsDispatch(service.PlatformAnthropic))
}

func TestTkOpenAIImageEditsDispatch_OpenAIAndGrokKeepNativeForwarders(t *testing.T) {
	require.Equal(t, "images", tkOpenAIImageEditsDispatch(service.PlatformOpenAI, false))
	require.Equal(t, "grok_images", tkOpenAIImageEditsDispatch(service.PlatformGrok, false))
}

func TestTkOpenAIImageEditsDispatch_NewAPIOpenAIRelayUsesImages(t *testing.T) {
	require.Equal(t, "images", tkOpenAIImageEditsDispatch(service.PlatformNewAPI, true))
}

func TestTkOpenAIImageEditsDispatch_OtherNewAPIStaysGated(t *testing.T) {
	require.Equal(t, "not_found", tkOpenAIImageEditsDispatch(service.PlatformNewAPI, false))
	require.Equal(t, "not_found", tkOpenAIImageEditsDispatch(service.PlatformAnthropic, true))
}
