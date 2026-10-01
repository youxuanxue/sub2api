package protocolrouter

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	_ "golang.org/x/image/webp"
)

// ProviderCapability describes provider semantics independently of HTTP/auth transport.
type ProviderCapability string

const (
	ProviderCapabilityGeneral   ProviderCapability = ""
	ProviderCapabilityGeminiWeb ProviderCapability = "gemini_web"
)

func (a AccountSnapshot) permitsProviderRequest(request CanonicalRequest) bool {
	if a.providerCapability != ProviderCapabilityGeminiWeb {
		return true
	}
	return GeminiWebRequestSupported(request, a.imageOutput, domain.IsGeminiProImageModel(a.resolvedModel))
}

// Web Pro reference requests have a narrower, verified conversion contract than
// the general Gemini text-only fallback. Reuse provider admission so this cannot
// admit tools, history, remote images or fields that the converters would drop.
func (a AccountSnapshot) preservesGeminiWebProReference(request CanonicalRequest, target Protocol) bool {
	return target == ProtocolGeminiGenerateContent &&
		a.providerCapability == ProviderCapabilityGeminiWeb &&
		domain.IsGeminiProImageModel(a.resolvedModel) &&
		request.profile.ContentKinds&ContentImage != 0 &&
		GeminiWebRequestSupported(request, a.imageOutput, true)
}

// GeminiWebRequestSupported validates provider-effective intent after the Plan
// has applied only authorized budget normalization. Streaming is buffered: the
// Worker emits its completed response as one SSE event.
func GeminiWebRequestSupported(request CanonicalRequest, image, proImage bool) bool {
	if request.inboundProtocol == ProtocolGeminiGenerateContent {
		body, ok := GeminiWebImageUpstreamBody(request.body, proImage)
		return ok && GeminiWebNativeBodySupported(body, image, proImage)
	}
	var root map[string]any
	if json.Unmarshal(request.body, &root) != nil {
		return false
	}
	allowed := []string{"model", "stream", "generationConfig"}
	var content any
	switch request.inboundProtocol {
	case ProtocolMessages, ProtocolChatCompletions:
		allowed = append(allowed, "messages")
		messages, ok := root["messages"].([]any)
		if !ok || len(messages) != 1 {
			return false
		}
		turn, ok := messages[0].(map[string]any)
		if !ok || !geminiWebOnlyKeys(turn, "role", "content") || turn["role"] != "user" {
			return false
		}
		content = turn["content"]
	case ProtocolResponses:
		if request.responsesPath != ResponsesPathRoot {
			return false
		}
		allowed = append(allowed, "input")
		switch input := root["input"].(type) {
		case string:
			content = input
		case []any:
			if len(input) != 1 {
				return false
			}
			turn, ok := input[0].(map[string]any)
			if !ok || !geminiWebOnlyKeys(turn, "type", "role", "content") || turn["role"] != "user" {
				return false
			}
			if kind, exists := turn["type"]; exists && kind != "message" {
				return false
			}
			content = turn["content"]
		default:
			return false
		}
	default:
		return false
	}
	if !geminiWebOnlyKeys(root, allowed...) {
		return false
	}
	if stream, exists := root["stream"]; exists {
		if _, ok := stream.(bool); !ok {
			return false
		}
	}
	parts := []any{}
	switch value := content.(type) {
	case string:
		parts = append(parts, map[string]any{"text": value})
	case []any:
		for _, raw := range value {
			part, ok := raw.(map[string]any)
			if ok && proImage && part["type"] != "text" && part["type"] != "input_text" {
				inline, valid := geminiWebCompatReference(part, request.inboundProtocol)
				if !valid {
					return false
				}
				parts = append(parts, map[string]any{"inlineData": inline})
				continue
			}
			if !ok || !geminiWebOnlyKeys(part, "type", "text") {
				return false
			}
			kind := "text"
			if request.inboundProtocol == ProtocolResponses {
				kind = "input_text"
			}
			if part["type"] != kind {
				return false
			}
			text, ok := part["text"].(string)
			if !ok {
				return false
			}
			parts = append(parts, map[string]any{"text": text})
		}
	default:
		return false
	}
	native := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": parts}}}
	if config, exists := root["generationConfig"]; exists {
		native["generationConfig"] = config
	}
	body, err := json.Marshal(native)
	if err != nil {
		return false
	}
	body, ok := GeminiWebImageUpstreamBody(body, proImage)
	return ok && GeminiWebNativeBodySupported(body, image, proImage)
}

// This is an admission projection of worker.request_prompt, not a converter.
// In particular it never flattens system/history, strips tools, or drops limits.
func GeminiWebNativeBodySupported(body []byte, image, proImage bool) bool {
	var root map[string]any
	if json.Unmarshal(body, &root) != nil || root == nil || !geminiWebOnlyKeys(root, "contents", "generationConfig") {
		return false
	}
	contents, ok := root["contents"].([]any)
	if !ok || len(contents) != 1 {
		return false
	}
	turn, ok := contents[0].(map[string]any)
	if !ok || !geminiWebOnlyKeys(turn, "role", "parts") {
		return false
	}
	if role, exists := turn["role"]; exists && role != "user" {
		return false
	}
	parts, ok := turn["parts"].([]any)
	if !ok {
		return false
	}
	texts := make([]string, 0, len(parts))
	imageCount, imageBytes := 0, 0
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok || len(part) != 1 {
			return false
		}
		if inline, exists := part["inlineData"]; exists && proImage {
			size, valid := geminiWebReferenceSize(inline)
			if !valid {
				return false
			}
			imageCount++
			imageBytes += size
			if imageCount > 4 || imageBytes > 10*1024*1024 {
				return false
			}
			continue
		}
		text, ok := part["text"].(string)
		if !ok {
			return false
		}
		texts = append(texts, text)
	}
	prompt := strings.Join(texts, "\n")
	// Python str.strip additionally treats these four separators as whitespace.
	blank := strings.TrimFunc(prompt, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= '\u001c' && r <= '\u001f')
	}) == ""
	if blank || utf8.RuneCountInString(prompt) > 32000 {
		return false
	}
	if raw, exists := root["generationConfig"]; exists {
		config, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		allowedConfigKeys := []string{"responseModalities"}
		if proImage {
			allowedConfigKeys = append(allowedConfigKeys, "candidateCount")
		}
		if image {
			allowedConfigKeys = append(allowedConfigKeys, "imageConfig")
		}
		if !geminiWebOnlyKeys(config, allowedConfigKeys...) {
			return false
		}
		if raw, exists := config["candidateCount"]; exists {
			if raw != float64(1) {
				return false
			}
		}
		if raw, exists := config["responseModalities"]; exists {
			modalities, ok := raw.([]any)
			if !ok || len(modalities) == 0 {
				return false
			}
			for _, mode := range modalities {
				if mode != "TEXT" && mode != "IMAGE" {
					return false
				}
			}
			hasImage := false
			for _, mode := range modalities {
				hasImage = hasImage || mode == "IMAGE"
			}
			if hasImage != image {
				return false
			}
		}
		if raw := config["imageConfig"]; raw != nil {
			imageConfig, ok := raw.(map[string]any)
			if !ok || !geminiWebOnlyKeys(imageConfig, "aspectRatio") {
				return false
			}
			ratio, ok := imageConfig["aspectRatio"].(string)
			if !ok || (!geminiWebImageAspectRatioSupported(ratio) && (!proImage || !slices.Contains(apicompat.GeminiImageAspectRatios(), ratio))) {
				return false
			}
		}
	}
	return true
}

func geminiWebImageAspectRatioSupported(ratio string) bool {
	switch ratio {
	case "1:1", "9:16", "3:4", "4:3", "16:9":
		return true
	default:
		return false
	}
}

func geminiWebOnlyKeys(object map[string]any, allowed ...string) bool {
	for key := range object {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found {
			return false
		}
	}
	return true
}

// GeminiWebImageUpstreamBody separates explicitly requested local image sizing
// from Google Web parameters. The execution retains the original size for the
// shared canvas postprocessor; relays must forward that intent to their edge.
func GeminiWebImageUpstreamBody(body []byte, proImage bool) ([]byte, bool) {
	if !proImage {
		return body, true
	}
	size := gjson.GetBytes(body, "generationConfig.imageConfig.imageSize")
	if !size.Exists() {
		return body, true
	}
	if size.Type != gjson.String || (size.Str != "1K" && size.Str != "2K" && size.Str != "4K") {
		return nil, false
	}
	next, err := sjson.DeleteBytes(body, "generationConfig.imageConfig.imageSize")
	if err != nil {
		return nil, false
	}
	cfg := gjson.GetBytes(next, "generationConfig.imageConfig")
	if cfg.IsObject() && len(cfg.Map()) == 0 {
		next, err = sjson.DeleteBytes(next, "generationConfig.imageConfig")
	}
	return next, err == nil
}

func geminiWebReferenceSize(raw any) (int, bool) {
	inline, ok := raw.(map[string]any)
	if !ok || !geminiWebOnlyKeys(inline, "mimeType", "data") {
		return 0, false
	}
	encoded, ok := inline["data"].(string)
	if !ok || len(encoded) > 14*1024*1024 {
		return 0, false
	}
	mime, ok := inline["mimeType"].(string)
	if !ok {
		return 0, false
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) > 10*1024*1024 {
		return 0, false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	expected := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp"}[format]
	return len(data), err == nil && expected != "" && expected == mime && cfg.Width > 0 && cfg.Height > 0 && int64(cfg.Width)*int64(cfg.Height) <= 16_000_000
}

func geminiWebCompatReference(part map[string]any, protocol Protocol) (map[string]any, bool) {
	if protocol == ProtocolMessages {
		source, ok := part["source"].(map[string]any)
		if !ok || !geminiWebOnlyKeys(part, "type", "source") || part["type"] != "image" || !geminiWebOnlyKeys(source, "type", "media_type", "data") || source["type"] != "base64" {
			return nil, false
		}
		return map[string]any{"mimeType": source["media_type"], "data": source["data"]}, true
	}
	var url string
	if protocol == ProtocolChatCompletions {
		ref, ok := part["image_url"].(map[string]any)
		if !ok || part["type"] != "image_url" || !geminiWebOnlyKeys(part, "type", "image_url") || !geminiWebOnlyKeys(ref, "url") {
			return nil, false
		}
		url, _ = ref["url"].(string)
	} else {
		if part["type"] != "input_image" || !geminiWebOnlyKeys(part, "type", "image_url") {
			return nil, false
		}
		url, _ = part["image_url"].(string)
	}
	if !strings.HasPrefix(url, "data:") {
		return nil, false
	}
	mime, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ";base64,")
	if !ok {
		return nil, false
	}
	return map[string]any{"mimeType": mime, "data": data}, true
}
