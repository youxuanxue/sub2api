package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

// gemini-native image aspect-ratio passthrough: a ClaudeRequest.ImageConfig on an image
// model must surface as generationConfig.imageConfig.aspectRatio on the wire to cloudcode-pa.
// The contract (which ratios upstream honors) is proven by the prod canary 2026-06-17; this
// guards that TK actually emits the field it dropped before.
func transformAspectRatio(t *testing.T, model string, cfg *ClaudeImageConfig) gjson.Result {
	t.Helper()
	req := &ClaudeRequest{
		Model:     model,
		MaxTokens: 1024,
		Messages: []ClaudeMessage{
			{Role: "user", Content: json.RawMessage(`"a red apple on a table"`)},
		},
		ImageConfig: cfg,
	}
	body, err := TransformClaudeToGeminiWithOptions(req, "project-1", model, DefaultTransformOptions())
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	// TransformClaudeToGeminiWithOptions returns the full v1internal envelope
	// ({project,…,request:{…,generationConfig}}), so the ratio lives under request.*.
	return gjson.GetBytes(body, "request.generationConfig.imageConfig.aspectRatio")
}

func TestAspectRatioPassthrough_ImageModel_Emitted(t *testing.T) {
	for _, model := range []string{
		"gemini-3.1-flash-image",
		"gemini-3.1-flash-image-preview",
		"gemini-2.5-flash-image",
		"gemini-3-pro-image",
	} {
		for _, ratio := range []string{"1:1", "16:9", "9:16", "21:9", "4:3"} {
			got := transformAspectRatio(t, model, &ClaudeImageConfig{AspectRatio: ratio})
			if got.String() != ratio {
				t.Errorf("model=%s ratio=%s: got imageConfig.aspectRatio=%q, want %q", model, ratio, got.String(), ratio)
			}
		}
	}
}

func TestAspectRatioPassthrough_NonImageModel_NotEmitted(t *testing.T) {
	// A chat model must never get an imageConfig even if a ratio leaks through.
	got := transformAspectRatio(t, "gemini-2.5-flash", &ClaudeImageConfig{AspectRatio: "16:9"})
	if got.Exists() {
		t.Errorf("chat model emitted imageConfig.aspectRatio=%q, want absent", got.String())
	}
}

func TestAspectRatioPassthrough_NoConfig_NotEmitted(t *testing.T) {
	// Image model without a ratio (the default-dimensions path) must not emit imageConfig.
	if got := transformAspectRatio(t, "gemini-3.1-flash-image", nil); got.Exists() {
		t.Errorf("nil ImageConfig emitted imageConfig.aspectRatio=%q, want absent", got.String())
	}
	if got := transformAspectRatio(t, "gemini-3.1-flash-image", &ClaudeImageConfig{AspectRatio: ""}); got.Exists() {
		t.Errorf("empty AspectRatio emitted imageConfig.aspectRatio=%q, want absent", got.String())
	}
}

func TestIsImageModel(t *testing.T) {
	cases := map[string]bool{
		"gemini-3.1-flash-image":         true,
		"gemini-3.1-flash-image-preview": true,
		"gemini-2.5-flash-image":         true,
		"gemini-3-pro-image":             true,
		"models/gemini-3.1-flash-image":  true,
		"nano-banana-pro":                true,
		"gemini-2.5-flash":               false,
		"gemini-3-flash-agent":           false,
		"claude-opus-4-8":                false,
		"":                               false,
	}
	for model, want := range cases {
		if got := IsImageModel(model); got != want {
			t.Errorf("IsImageModel(%q)=%v, want %v", model, got, want)
		}
	}
}
