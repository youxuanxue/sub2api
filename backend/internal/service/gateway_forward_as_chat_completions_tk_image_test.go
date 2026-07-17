//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
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

// End-to-end wire contract: the whole feature hinges on the prod-side injection key
// (image_config.aspect_ratio) matching the edge-side antigravity.ClaudeRequest json tags.
// The two halves are otherwise tested in isolation (the transform test sets ImageConfig via
// the Go field, bypassing JSON), so a rename of either json tag would silently break the
// passthrough with every other test still green. This crosses the real boundary: inject onto
// an Anthropic body, unmarshal it into ClaudeRequest exactly as Forward does, run the
// transform, and assert the ratio reaches generationConfig.imageConfig.aspectRatio.
func TestGeminiAspectRatio_ProdToEdgeWireContract(t *testing.T) {
	const ratio = "16:9"
	const model = "gemini-3.1-flash-image"
	cc := []byte(`{"model":"` + model + `","messages":[],"extra_body":{"google":{"image_config":{"aspect_ratio":"` + ratio + `"}}}}`)
	anthropic := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"a red apple"}],"max_tokens":1024}`)

	relayed := tkInjectGeminiImageAspectRatio(cc, anthropic)

	// Edge: Forward unmarshals the relayed body into a ClaudeRequest.
	var claudeReq antigravity.ClaudeRequest
	if err := json.Unmarshal(relayed, &claudeReq); err != nil {
		t.Fatalf("unmarshal relayed body into ClaudeRequest: %v", err)
	}
	if claudeReq.ImageConfig == nil || claudeReq.ImageConfig.AspectRatio != ratio {
		t.Fatalf("ClaudeRequest.ImageConfig did not carry the ratio: %+v (json key drift between inject and ClaudeRequest tags?)", claudeReq.ImageConfig)
	}

	// Edge: transform emits it onto the cloudcode-pa wire.
	body, err := antigravity.TransformClaudeToGeminiWithOptions(&claudeReq, "p", model, antigravity.DefaultTransformOptions())
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if got := gjson.GetBytes(body, "request.generationConfig.imageConfig.aspectRatio").String(); got != ratio {
		t.Fatalf("upstream wire aspectRatio=%q, want %q", got, ratio)
	}
}
