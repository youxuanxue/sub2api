package service

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// preserveGeminiCompatOptions keeps the native image extension across the typed
// OpenAI -> Anthropic bridge. Anthropic's required default token limit is not a
// client instruction and must not leak into a Gemini request without a limit.
func preserveGeminiCompatOptions(original, converted []byte) ([]byte, error) {
	var source, target map[string]any
	if err := json.Unmarshal(original, &source); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(converted, &target); err != nil {
		return nil, err
	}
	options, err := geminiCompatGenerationOptions(source)
	if err != nil {
		return nil, err
	}
	if len(options) > 0 {
		target["generationConfig"] = options
	}
	hasLimit := false
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if value, ok := source[key]; ok && value != nil {
			hasLimit = true
		}
	}
	if !hasLimit {
		delete(target, "max_tokens")
	}
	return json.Marshal(target)
}

// generationConfig is the native extension spelling for compatibility clients.
// Studio / OpenAI-compat also send extra_body.google.image_config.{aspect_ratio,image_size};
// those spellings are lifted here so the Anthropic bridge and Gemini converter see
// one generationConfig. Explicit null imageConfig must survive as null unless a
// concrete field is being merged in.
func geminiCompatGenerationOptions(req map[string]any) (map[string]any, error) {
	var config map[string]any
	if value, exists := req["generationConfig"]; exists {
		var ok bool
		config, ok = value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("generationConfig must be an object")
		}
		for key, value := range config {
			switch key {
			case "responseModalities":
				modalities, ok := value.([]any)
				if !ok || len(modalities) == 0 {
					return nil, fmt.Errorf("generationConfig.responseModalities must be a nonempty array")
				}
				for _, modality := range modalities {
					if modality != "TEXT" && modality != "IMAGE" {
						return nil, fmt.Errorf("generationConfig.responseModalities supports TEXT and IMAGE")
					}
				}
			case "imageConfig":
				if value != nil {
					if _, ok := value.(map[string]any); !ok {
						return nil, fmt.Errorf("generationConfig.imageConfig must be an object or null")
					}
				}
			default:
				return nil, fmt.Errorf("unsupported compatibility generationConfig field %q", key)
			}
		}
	}
	ratio, size, present, err := geminiCompatExtraBodyImageConfig(req)
	if err != nil {
		return nil, err
	}
	if !present || (ratio == "" && size == "") {
		return config, nil
	}
	if config == nil {
		config = make(map[string]any)
	}
	imageConfig, _ := config["imageConfig"].(map[string]any)
	if imageConfig == nil {
		imageConfig = make(map[string]any)
	}
	if ratio != "" {
		if _, has := imageConfig["aspectRatio"]; !has {
			imageConfig["aspectRatio"] = ratio
		}
	}
	if size != "" {
		if _, has := imageConfig["imageSize"]; !has {
			imageConfig["imageSize"] = size
		}
	}
	if len(imageConfig) > 0 {
		config["imageConfig"] = imageConfig
	}
	return config, nil
}

// geminiCompatExtraBodyImageConfig reads the OpenAI Studio image-config spelling
// the same way Antigravity's tkInject does: only the known paths are lifted.
// Unrelated extra_body keys stay ignored so non-Web Gemini clients that stuff
// SDK baggage into extra_body keep working. Web admission remains strict via
// geminiWebNormalizeChatExtraBody (image_size is rejected on Web-only paths).
func geminiCompatExtraBodyImageConfig(req map[string]any) (ratio, size string, present bool, err error) {
	raw, exists := req["extra_body"]
	if !exists {
		return "", "", false, nil
	}
	extra, ok := raw.(map[string]any)
	if !ok {
		return "", "", false, nil
	}
	google, _ := extra["google"].(map[string]any)
	if google == nil {
		return "", "", false, nil
	}
	imageConfig, _ := google["image_config"].(map[string]any)
	if imageConfig == nil {
		return "", "", false, nil
	}
	if ratioRaw, exists := imageConfig["aspect_ratio"]; exists {
		ratioStr, ok := ratioRaw.(string)
		if !ok {
			return "", "", false, fmt.Errorf("extra_body.google.image_config.aspect_ratio must be a string")
		}
		ratio = strings.TrimSpace(ratioStr)
		present = true
	}
	if sizeRaw, exists := imageConfig["image_size"]; exists {
		sizeStr, ok := sizeRaw.(string)
		if !ok {
			return "", "", false, fmt.Errorf("extra_body.google.image_config.image_size must be a string")
		}
		size = strings.TrimSpace(sizeStr)
		present = true
	}
	return ratio, size, present, nil
}

// geminiInlineImageMarkdown is shared by all client response encoders and the
// accounting observer: an invalid image can neither be emitted nor billed.
func geminiInlineImageMarkdown(part map[string]any) (string, bool) {
	inline, ok := part["inlineData"].(map[string]any)
	if !ok {
		inline, ok = part["inline_data"].(map[string]any)
	}
	if !ok {
		return "", false
	}
	mimeType, _ := inline["mimeType"].(string)
	if mimeType == "" {
		mimeType, _ = inline["mime_type"].(string)
	}
	data, _ := inline["data"].(string)
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if !isGeminiInlineImageMIMEType(mimeType) || !isValidBase64(data) {
		return "", false
	}
	return fmt.Sprintf("![image](data:%s;base64,%s)", mimeType, data), true
}

// A stream may repeat cumulative image parts. Keep the maximum multiplicity
// per image while retaining distinct images arriving in separate events.
// Identical images in separate delta events are inherently ambiguous; counting
// them once avoids billing the same cumulative image repeatedly.
type geminiImageMultiplicity struct {
	seen  map[[32]byte]int
	count int
}

func newGeminiImageMultiplicity() *geminiImageMultiplicity {
	return &geminiImageMultiplicity{seen: make(map[[32]byte]int)}
}

// observe returns which occurrences have not already appeared in earlier events.
func (m *geminiImageMultiplicity) observe(images []string) []bool {
	accepted := make([]bool, len(images))
	occurrences := make(map[[32]byte]int)
	for i, image := range images {
		key := sha256.Sum256([]byte(image))
		occurrences[key]++
		accepted[i] = occurrences[key] > m.seen[key]
	}
	for key, count := range occurrences {
		if count > m.seen[key] {
			m.count += count - m.seen[key]
			m.seen[key] = count
		}
	}
	return accepted
}

type geminiImageStream struct{ images *geminiImageMultiplicity }

func newGeminiImageStream() *geminiImageStream {
	return &geminiImageStream{images: newGeminiImageMultiplicity()}
}
func (s *geminiImageStream) textParts(parts []map[string]any) []map[string]any {
	return s.parts(parts, true)
}
func (s *geminiImageStream) parts(parts []map[string]any, asText bool) []map[string]any {
	images := make([]string, 0, len(parts))
	for _, part := range parts {
		if markdown, ok := geminiInlineImageMarkdown(part); ok {
			images = append(images, markdown)
		}
	}
	accepted := s.images.observe(images)
	imageIndex := 0
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if _, ok := part["text"]; ok {
			out = append(out, part)
		} else if _, ok := part["functionCall"]; ok {
			out = append(out, part)
		}
		if markdown, ok := geminiInlineImageMarkdown(part); ok {
			if accepted[imageIndex] {
				if asText {
					out = append(out, map[string]any{"text": markdown, "gemini_inline_image": true})
				} else {
					out = append(out, part)
				}
			}
			imageIndex++
		}
	}
	return out
}

// Reject unsupported/corrupt inline output instead of returning a successful
// compatibility envelope that silently loses its only generated image.
func validateGeminiCompatImageResponse(response map[string]any) error {
	return validateGeminiImageResponse(response, false)
}

// errGeminiImageModelEmpty is returned when an image model completes without any
// inline image. Callers must treat it as retryable upstream vacuum (same class as
// OpenAI Images no_image_output) and must not write a successful 200 empty body.
var errGeminiImageModelEmpty = errors.New("image model returned no image output")

func requireGeminiImageModelOutput(model string, response map[string]any) error {
	if !antigravity.IsImageModel(model) {
		return nil
	}
	if geminiResponseInlineImageCount(response) == 0 {
		return errGeminiImageModelEmpty
	}
	return nil
}

// requireGeminiImageModelStreamOutput fails closed when an image model stream
// finished without any validated inline image. Callers must return this before
// writing terminal success events ([DONE] / message_stop); after headers are
// committed failover is unsafe, but billing must still see a non-nil error.
func requireGeminiImageModelStreamOutput(model string, c *gin.Context) error {
	if !antigravity.IsImageModel(model) {
		return nil
	}
	if observedGeminiImageOutputs(c) == 0 {
		return errGeminiImageModelEmpty
	}
	return nil
}

func geminiResponseInlineImageCount(response map[string]any) int {
	count := 0
	for _, part := range extractGeminiParts(response) {
		if _, ok := geminiInlineImageMarkdown(part); ok {
			count++
		}
	}
	return count
}

func summarizeGeminiEmptyImageBody(body []byte) string {
	if len(body) == 0 {
		return "empty_body"
	}
	finish := gjson.GetBytes(body, "candidates.0.finishReason").String()
	promptBlock := gjson.GetBytes(body, "promptFeedback.blockReason").String()
	parts := gjson.GetBytes(body, "candidates.0.content.parts").Array()
	return fmt.Sprintf("finish=%s prompt_block=%s parts=%d body_len=%d", finish, promptBlock, len(parts), len(body))
}

func validateGeminiImageResponse(response map[string]any, native bool) error {
	for _, part := range extractGeminiParts(response) {
		_, camel := part["inlineData"]
		_, snake := part["inline_data"]
		if camel || snake {
			// Native Gemini transports may also carry audio/video inline media.
			// Their existing native contract is independent of this image converter.
			if native {
				inline, _ := part["inlineData"].(map[string]any)
				if inline == nil {
					inline, _ = part["inline_data"].(map[string]any)
				}
				mimeType, _ := inline["mimeType"].(string)
				if mimeType == "" {
					mimeType, _ = inline["mime_type"].(string)
				}
				if mimeType != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "image/") {
					continue
				}
			}
			if _, valid := geminiInlineImageMarkdown(part); !valid {
				return fmt.Errorf("upstream returned unsupported or malformed inline image data")
			}
		}
	}
	return nil
}
