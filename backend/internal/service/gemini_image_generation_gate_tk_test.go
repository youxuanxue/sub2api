//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTKGeminiImageGroupSwitchDoesNotDenyDefaultOffGroups(t *testing.T) {
	require.False(t, tkGeminiImageGroupSwitchRetroactive())
	group := &Group{Platform: PlatformGemini, AllowImageGeneration: false}
	require.False(t, GeminiImageGenerationDenied(group, []byte(`{"generationConfig":{"responseModalities":["IMAGE"]}}`), "gemini-3-pro-image"))
}
