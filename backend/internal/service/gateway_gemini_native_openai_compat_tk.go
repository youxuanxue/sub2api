package service

import "github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"

// UsesGeminiNativeOpenAICompat reports whether OpenAI chat/responses requests
// should be forwarded through GeminiMessagesCompatService.
func UsesGeminiNativeOpenAICompat(platform, model string) bool {
	switch platform {
	case PlatformGemini:
		return true
	case PlatformAntigravity:
		return !antigravity.IsImageModel(model)
	default:
		return false
	}
}
