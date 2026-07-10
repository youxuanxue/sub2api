package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

// UsesGeminiNativeOpenAICompat reports whether OpenAI chat/responses requests
// should be forwarded through GeminiMessagesCompatService.
func UsesGeminiNativeOpenAICompat(platform, model string) bool {
	switch platform {
	case PlatformGemini:
		return true
	case PlatformAntigravity:
		return isAntigravityGeminiNativeTextModel(model)
	default:
		return false
	}
}

func isAntigravityGeminiNativeTextModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	return strings.HasPrefix(m, "gemini-") && !antigravity.IsImageModel(model)
}
