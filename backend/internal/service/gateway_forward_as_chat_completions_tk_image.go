package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// tkInjectGeminiImageConfig threads gemini-native image config from an OpenAI Chat
// Completions inbound onto the Anthropic /v1/messages body that
// GatewayService.ForwardAsChatCompletions relays upstream.
//
// The inbound carries fields at extra_body.google.image_config.{aspect_ratio,image_size}
// — fields apicompat.ChatCompletionsRequest does not model, so they are invisible to the
// CC→Responses→Anthropic struct chain. We lift them off the raw CC body and stamp them
// onto the relayed Anthropic body as image_config.*. Downstream the antigravity transform
// reads ClaudeRequest.ImageConfig and emits generationConfig.imageConfig to cloudcode-pa.
//
// No-op unless the inbound carried at least one non-empty field.
func tkInjectGeminiImageAspectRatio(ccBody, anthropicBody []byte) []byte {
	return tkInjectGeminiImageConfig(ccBody, anthropicBody)
}

func tkInjectGeminiImageConfig(ccBody, anthropicBody []byte) []byte {
	ar := strings.TrimSpace(gjson.GetBytes(ccBody, "extra_body.google.image_config.aspect_ratio").String())
	size := strings.TrimSpace(gjson.GetBytes(ccBody, "extra_body.google.image_config.image_size").String())
	if ar == "" && size == "" {
		return anthropicBody
	}
	out := anthropicBody
	var err error
	if ar != "" {
		out, err = sjson.SetBytes(out, "image_config.aspect_ratio", ar)
		if err != nil {
			return anthropicBody
		}
	}
	if size != "" {
		out, err = sjson.SetBytes(out, "image_config.image_size", size)
		if err != nil {
			return anthropicBody
		}
	}
	return out
}
