package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// tkInjectGeminiImageAspectRatio threads a gemini-native image aspect ratio from an
// OpenAI Chat Completions inbound onto the Anthropic /v1/messages body that the
// OpenAI-compat path (GatewayService.ForwardAsChatCompletions) relays upstream.
//
// The inbound carries the ratio at extra_body.google.image_config.aspect_ratio — a field
// apicompat.ChatCompletionsRequest does not model, so it is invisible to the CC→Responses→
// Anthropic struct chain (same situation as reasoning_effort, which the call site already
// re-reads from the raw body via gjson). We therefore lift it straight off the raw CC body
// and stamp it onto the relayed Anthropic body as image_config.aspect_ratio. Downstream the
// antigravity transform (internal/pkg/antigravity) reads ClaudeRequest.ImageConfig and emits
// generationConfig.imageConfig.aspectRatio to cloudcode-pa, which honors all 10 documented
// ratios (prod canary 2026-06-17).
//
// No-op unless the inbound carried a non-empty aspect_ratio, so non-image / ratio-less
// traffic is untouched. Pure over bytes to keep the ForwardAsChatCompletions call site to a
// single line and the behavior unit-testable.
func tkInjectGeminiImageAspectRatio(ccBody, anthropicBody []byte) []byte {
	ar := strings.TrimSpace(gjson.GetBytes(ccBody, "extra_body.google.image_config.aspect_ratio").String())
	if ar == "" {
		return anthropicBody
	}
	out, err := sjson.SetBytes(anthropicBody, "image_config.aspect_ratio", ar)
	if err != nil {
		return anthropicBody
	}
	return out
}
