package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestTkOpenAIImageGenerationsDispatch_PlatformOpenAIUsesImages(t *testing.T) {
	require.Equal(t, "images", tkOpenAIImageGenerationsDispatch(service.PlatformOpenAI))
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
