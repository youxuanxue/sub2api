//go:build unit

package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

// The OpenAI-compat path must lift extra_body.google.image_config.aspect_ratio off the raw
// CC body and stamp it onto the relayed Anthropic body as image_config.aspect_ratio (which
// apicompat.ChatCompletionsRequest does not model). The antigravity transform reads it
// downstream. No-op for ratio-less inbounds.
func TestTkInjectGeminiImageAspectRatio(t *testing.T) {
	anthropic := []byte(`{"model":"gemini-3.1-flash-image","messages":[{"role":"user","content":"hi"}]}`)

	t.Run("injects when present", func(t *testing.T) {
		cc := []byte(`{"model":"gemini-3.1-flash-image","messages":[],"extra_body":{"google":{"image_config":{"aspect_ratio":"16:9"}}}}`)
		out := tkInjectGeminiImageAspectRatio(cc, anthropic)
		if got := gjson.GetBytes(out, "image_config.aspect_ratio").String(); got != "16:9" {
			t.Errorf("image_config.aspect_ratio=%q, want 16:9", got)
		}
		// Original anthropic fields preserved.
		if got := gjson.GetBytes(out, "model").String(); got != "gemini-3.1-flash-image" {
			t.Errorf("model clobbered: %q", got)
		}
	})

	t.Run("no-op when absent", func(t *testing.T) {
		cc := []byte(`{"model":"gemini-3.1-flash-image","messages":[]}`)
		out := tkInjectGeminiImageAspectRatio(cc, anthropic)
		if gjson.GetBytes(out, "image_config").Exists() {
			t.Errorf("image_config injected for ratio-less inbound: %s", out)
		}
	})

	t.Run("no-op when empty string", func(t *testing.T) {
		cc := []byte(`{"extra_body":{"google":{"image_config":{"aspect_ratio":""}}}}`)
		out := tkInjectGeminiImageAspectRatio(cc, anthropic)
		if gjson.GetBytes(out, "image_config").Exists() {
			t.Errorf("image_config injected for empty ratio: %s", out)
		}
	})
}
