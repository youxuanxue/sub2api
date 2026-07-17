//go:build unit

package service

import "testing"

func TestUsesGeminiNativeOpenAICompat(t *testing.T) {
	t.Run("gemini always uses compat bridge", func(t *testing.T) {
		if !UsesGeminiNativeOpenAICompat(PlatformGemini, "gemini-2.5-flash") {
			t.Fatalf("expected gemini to use compat bridge")
		}
	})

	t.Run("antigravity gemini text uses compat bridge", func(t *testing.T) {
		if !UsesGeminiNativeOpenAICompat(PlatformAntigravity, "gemini-3.5-flash") {
			t.Fatalf("expected antigravity text to use compat bridge")
		}
	})

	t.Run("antigravity claude uses messages bridge", func(t *testing.T) {
		if UsesGeminiNativeOpenAICompat(PlatformAntigravity, "claude-sonnet-4-6") {
			t.Fatalf("expected antigravity Claude model to use messages bridge")
		}
	})

	t.Run("antigravity models-prefixed gemini text uses compat bridge", func(t *testing.T) {
		if !UsesGeminiNativeOpenAICompat(PlatformAntigravity, "models/gemini-pro-agent") {
			t.Fatalf("expected prefixed antigravity Gemini text to use compat bridge")
		}
	})

	t.Run("antigravity image stays non-compat path", func(t *testing.T) {
		if UsesGeminiNativeOpenAICompat(PlatformAntigravity, "gemini-3.1-flash-image") {
			t.Fatalf("expected antigravity image to bypass compat bridge")
		}
	})

	t.Run("other platforms never use compat bridge", func(t *testing.T) {
		if UsesGeminiNativeOpenAICompat(PlatformOpenAI, "gpt-5") {
			t.Fatalf("expected non-gemini platforms to bypass compat bridge")
		}
	})
}
