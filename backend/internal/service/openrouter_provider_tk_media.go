package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenRouterImageRoute selects the downstream handler family for OR seller POST /v1/images.
type OpenRouterImageRoute string

const (
	OpenRouterImageRouteAntigravityChat OpenRouterImageRoute = "antigravity_chat"
	OpenRouterImageRouteGrok            OpenRouterImageRoute = "grok"
	OpenRouterImageRouteOpenAICompat    OpenRouterImageRoute = "openai_compat"
)

var openRouterChatCompletionDataURIRe = regexp.MustCompile(`data:([^;)\s]+);base64,([A-Za-z0-9+/=]+)`)

// OpenRouterProviderImageRoute picks the backend pipeline for OR seller POST /v1/images.
// Route by model identity (after stripping public tokenkey/ prefix). Do NOT use
// apiKey.Group.Platform: OR inference keys are universal (group_id NULL) and platform
// is only known after later universal routing — waiting on Group.Platform sent every
// gemini-*-image request down the OpenAI images path as "Unsupported model".
func OpenRouterProviderImageRoute(model string) OpenRouterImageRoute {
	model = openRouterProviderImageRouteModelID(model)
	switch {
	case antigravity.IsImageModel(model):
		return OpenRouterImageRouteAntigravityChat
	case isGrokImageGenerationModel(model):
		return OpenRouterImageRouteGrok
	default:
		return OpenRouterImageRouteOpenAICompat
	}
}

func openRouterProviderImageRouteModelID(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, "tokenkey/")
	model = strings.TrimPrefix(model, "models/")
	return model
}

// TranslateOpenRouterImageToChatCompletions maps OR POST /v1/images to OpenAI chat
// completions for antigravity-native gemini-*-image models (generateContent path).
func TranslateOpenRouterImageToChatCompletions(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("invalid openrouter image request body")
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}
	prompt := strings.TrimSpace(gjson.GetBytes(body, "prompt").String())
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	out := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{"role": "user", "content": prompt},
		},
		"stream": false,
	}
	if aspectRatio := strings.TrimSpace(gjson.GetBytes(body, "aspect_ratio").String()); aspectRatio != "" {
		out["extra_body"] = map[string]any{
			"google": map[string]any{
				"image_config": map[string]any{
					"aspect_ratio": aspectRatio,
				},
			},
		}
	}
	return json.Marshal(out)
}

// TranslateChatCompletionsImageResponseToOpenRouter extracts inline images from a
// chat-completions JSON payload and maps them to OR ImageGenerationResponse.
func TranslateChatCompletionsImageResponseToOpenRouter(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, nil
	}
	if gjson.GetBytes(body, "error").Exists() {
		return body, nil
	}
	items := extractOpenRouterImagesFromChatCompletions(body)
	if len(items) == 0 {
		return nil, fmt.Errorf("chat completion response missing image data")
	}
	out := map[string]any{
		"created": time.Now().Unix(),
		"data":    items,
	}
	if usage := gjson.GetBytes(body, "usage"); usage.Exists() {
		out["usage"] = json.RawMessage(usage.Raw)
	}
	return json.Marshal(out)
}

func extractOpenRouterImagesFromChatCompletions(body []byte) []map[string]string {
	content := gjson.GetBytes(body, "choices.0.message.content")
	if !content.Exists() {
		return nil
	}
	switch content.Type {
	case gjson.String:
		return extractOpenRouterImagesFromChatContentString(content.String())
	case gjson.JSON:
		if content.IsArray() {
			return extractOpenRouterImagesFromChatContentParts(content)
		}
	}
	return nil
}

func extractOpenRouterImagesFromChatContentString(content string) []map[string]string {
	matches := openRouterChatCompletionDataURIRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	items := make([]map[string]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		items = append(items, map[string]string{
			"b64_json":   match[2],
			"media_type": strings.TrimSpace(match[1]),
		})
	}
	return items
}

func extractOpenRouterImagesFromChatContentParts(content gjson.Result) []map[string]string {
	items := make([]map[string]string, 0)
	content.ForEach(func(_, part gjson.Result) bool {
		partType := strings.TrimSpace(part.Get("type").String())
		switch partType {
		case "image_url":
			url := strings.TrimSpace(part.Get("image_url.url").String())
			if mt, payload, ok := parseDataURI(url); ok && payload != "" {
				entry := map[string]string{"b64_json": payload}
				if mt != "" {
					entry["media_type"] = mt
				}
				items = append(items, entry)
			}
		case "text":
			items = append(items, extractOpenRouterImagesFromChatContentString(part.Get("text").String())...)
		}
		return true
	})
	return items
}

// TranslateOpenRouterImageRequestToOpenAI maps OpenRouter POST /v1/images JSON to
// TokenKey's OpenAI-compatible POST /v1/images/generations body.
func TranslateOpenRouterImageRequestToOpenAI(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("invalid openrouter image request body")
	}
	out := []byte(`{}`)
	for _, key := range []string{"model", "prompt", "n", "seed", "quality", "background", "output_format", "output_compression"} {
		if v := gjson.GetBytes(body, key); v.Exists() {
			var err error
			out, err = sjson.SetRawBytes(out, key, []byte(v.Raw))
			if err != nil {
				return nil, err
			}
		}
	}
	if size := gjson.GetBytes(body, "size"); size.Exists() && size.String() != "" {
		out, _ = sjson.SetBytes(out, "size", size.String())
	} else if resolution := gjson.GetBytes(body, "resolution"); resolution.Exists() && resolution.String() != "" {
		out, _ = sjson.SetBytes(out, "size", resolution.String())
	}
	if aspectRatio := gjson.GetBytes(body, "aspect_ratio"); aspectRatio.Exists() && aspectRatio.String() != "" {
		out, _ = sjson.SetBytes(out, "aspect_ratio", aspectRatio.String())
	}
	if refs := gjson.GetBytes(body, "input_references"); refs.Exists() {
		out, _ = sjson.SetRawBytes(out, "input_references", []byte(refs.Raw))
	}
	out, _ = sjson.SetBytes(out, "response_format", "b64_json")
	return out, nil
}

// TranslateOpenAIImageResponseToOpenRouter maps OpenAI images/generations JSON to
// OpenRouter ImageGenerationResponse ({created, data:[{b64_json, media_type?}]}).
func TranslateOpenAIImageResponseToOpenRouter(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, nil
	}
	if gjson.GetBytes(body, "error").Exists() {
		return body, nil
	}
	created := gjson.GetBytes(body, "created").Int()
	if created <= 0 {
		created = time.Now().Unix()
	}
	data := gjson.GetBytes(body, "data")
	if !data.IsArray() {
		return body, nil
	}
	items := make([]map[string]string, 0, len(data.Array()))
	data.ForEach(func(_, item gjson.Result) bool {
		b64 := strings.TrimSpace(item.Get("b64_json").String())
		if b64 == "" {
			return true
		}
		entry := map[string]string{"b64_json": b64}
		if format := strings.TrimSpace(item.Get("output_format").String()); format != "" {
			entry["media_type"] = openRouterImageMediaType(format)
		} else if url := item.Get("url").String(); strings.HasPrefix(url, "data:") {
			if mt, payload, ok := parseDataURI(url); ok && payload != "" {
				entry["b64_json"] = payload
				if mt != "" {
					entry["media_type"] = mt
				}
			}
		}
		items = append(items, entry)
		return true
	})
	if len(items) == 0 {
		return body, nil
	}
	out := map[string]any{
		"created": created,
		"data":    items,
	}
	if usage := gjson.GetBytes(body, "usage"); usage.Exists() {
		out["usage"] = json.RawMessage(usage.Raw)
	}
	return json.Marshal(out)
}

func openRouterImageMediaType(outputFormat string) string {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "svg":
		return "image/svg+xml"
	default:
		return "image/png"
	}
}

func parseDataURI(uri string) (mediaType, payload string, ok bool) {
	uri = strings.TrimSpace(uri)
	if !strings.HasPrefix(uri, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(uri, "data:")
	comma := strings.Index(rest, ",")
	if comma <= 0 {
		return "", "", false
	}
	meta, data := rest[:comma], rest[comma+1:]
	meta = strings.TrimSuffix(meta, ";base64")
	return meta, data, data != ""
}

// TranslateOpenRouterVideoRequestToOpenAI maps OpenRouter POST /v1/videos JSON to
// TokenKey's OpenAI-compatible video submit body.
func TranslateOpenRouterVideoRequestToOpenAI(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("invalid openrouter video request body")
	}
	out := []byte(`{}`)
	for _, key := range []string{"model", "prompt", "seed", "callback_url"} {
		if v := gjson.GetBytes(body, key); v.Exists() {
			var err error
			out, err = sjson.SetRawBytes(out, key, []byte(v.Raw))
			if err != nil {
				return nil, err
			}
		}
	}
	if duration := gjson.GetBytes(body, "duration"); duration.Exists() && duration.Float() > 0 {
		seconds := int64(duration.Float() + 0.5)
		if seconds < 1 {
			seconds = 1
		}
		if seconds > 60 {
			seconds = 60
		}
		out, _ = sjson.SetBytes(out, "seconds", fmt.Sprintf("%d", seconds))
	}
	if size := gjson.GetBytes(body, "size"); size.Exists() && size.String() != "" {
		out, _ = sjson.SetBytes(out, "size", size.String())
	} else if resolution := gjson.GetBytes(body, "resolution"); resolution.Exists() && resolution.String() != "" {
		out, _ = sjson.SetBytes(out, "size", resolution.String())
	}
	if aspectRatio := gjson.GetBytes(body, "aspect_ratio"); aspectRatio.Exists() && aspectRatio.String() != "" {
		out, _ = sjson.SetBytes(out, "aspect_ratio", aspectRatio.String())
	}
	if generateAudio := gjson.GetBytes(body, "generate_audio"); generateAudio.Exists() {
		out, _ = sjson.SetRawBytes(out, "generate_audio", []byte(generateAudio.Raw))
	}
	contentParts := openRouterVideoContentParts(body)
	if len(contentParts) > 0 {
		raw, err := json.Marshal(contentParts)
		if err != nil {
			return nil, err
		}
		out, _ = sjson.SetRawBytes(out, "metadata", mustJSONSetMetadataContent(raw))
	}
	return out, nil
}

func mustJSONSetMetadataContent(content []byte) []byte {
	md, _ := sjson.SetRawBytes([]byte(`{}`), "content", content)
	return md
}

func openRouterVideoContentParts(body []byte) []map[string]any {
	out := make([]map[string]any, 0, 4)
	appendImageParts := func(raw gjson.Result) {
		if !raw.IsArray() {
			return
		}
		raw.ForEach(func(_, item gjson.Result) bool {
			if item.Get("image_url").Exists() {
				part := map[string]any{"type": "image_url"}
				if url := item.Get("image_url.url"); url.Exists() {
					part["image_url"] = map[string]string{"url": url.String()}
				} else {
					part["image_url"] = json.RawMessage(item.Get("image_url").Raw)
				}
				out = append(out, part)
				return true
			}
			if item.Get("type").String() == "image_url" {
				out = append(out, jsonObjectFromResult(item))
				return true
			}
			return true
		})
	}
	if frames := gjson.GetBytes(body, "frame_images"); frames.Exists() {
		frames.ForEach(func(_, item gjson.Result) bool {
			if item.Get("image_url").Exists() || item.Get("url").Exists() {
				url := item.Get("image_url.url").String()
				if url == "" {
					url = item.Get("url").String()
				}
				if url != "" {
					out = append(out, map[string]any{
						"type":      "image_url",
						"image_url": map[string]string{"url": url},
					})
				}
			}
			return true
		})
	}
	if refs := gjson.GetBytes(body, "input_references"); refs.Exists() {
		appendImageParts(refs)
	}
	return out
}

func jsonObjectFromResult(v gjson.Result) map[string]any {
	var out map[string]any
	_ = json.Unmarshal([]byte(v.Raw), &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// BuildOpenRouterVideoSubmitResponse builds the OpenRouter 202 submit payload.
func BuildOpenRouterVideoSubmitResponse(taskID, apiBase string) ([]byte, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	pollingURL := OpenRouterProviderVideoPollURL(apiBase, taskID)
	out := map[string]string{
		"id":          taskID,
		"polling_url": pollingURL,
		"status":      "pending",
	}
	return json.Marshal(out)
}

// OpenRouterProviderVideoPollURL returns the seller poll URL for a public task id.
func OpenRouterProviderVideoPollURL(apiBase, taskID string) string {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		base = "https://api.tokenkey.dev"
	}
	return base + "/openrouter/v1/videos/" + strings.TrimSpace(taskID)
}

// TranslateOpenAIVideoFetchToOpenRouter maps TokenKey/OpenAI video poll JSON to
// OpenRouter VideoGenerationResponse.
func TranslateOpenAIVideoFetchToOpenRouter(taskID string, body []byte, apiBase string) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, nil
	}
	if gjson.GetBytes(body, "error").Exists() {
		return body, nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		taskID = strings.TrimSpace(gjson.GetBytes(body, "id").String())
	}
	status := mapOpenAIVideoStatusToOpenRouter(gjson.GetBytes(body, "status").String())
	out := map[string]any{
		"id":          taskID,
		"polling_url": OpenRouterProviderVideoPollURL(apiBase, taskID),
		"status":      status,
	}
	if errMsg := openRouterVideoErrorMessage(body); errMsg != "" {
		out["error"] = errMsg
	}
	if urls := openRouterVideoUnsignedURLs(body, apiBase, taskID); len(urls) > 0 {
		out["unsigned_urls"] = urls
	}
	if genID := strings.TrimSpace(gjson.GetBytes(body, "generation_id").String()); genID != "" {
		out["generation_id"] = genID
	}
	return json.Marshal(out)
}

func mapOpenAIVideoStatusToOpenRouter(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "not_start", "submitted", "unknown", "pending":
		return "pending"
	case "in_progress", "processing":
		return "in_progress"
	case "completed", "success", "succeeded":
		return "completed"
	case "failed", "failure":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "expired":
		return "expired"
	default:
		if status == "" {
			return "pending"
		}
		return status
	}
}

func openRouterVideoErrorMessage(body []byte) string {
	if msg := strings.TrimSpace(gjson.GetBytes(body, "error.message").String()); msg != "" {
		return msg
	}
	return strings.TrimSpace(gjson.GetBytes(body, "error").String())
}

func openRouterVideoUnsignedURLs(body []byte, apiBase, taskID string) []string {
	candidates := []string{
		gjson.GetBytes(body, "video_url").String(),
		gjson.GetBytes(body, "url").String(),
		gjson.GetBytes(body, "download_url").String(),
		gjson.GetBytes(body, "content.video_url").String(),
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.HasPrefix(candidate, "/") {
			base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
			if base != "" {
				candidate = base + candidate
			}
		}
		return []string{candidate}
	}
	if gjson.GetBytes(body, "unsigned_urls").IsArray() {
		urls := make([]string, 0)
		gjson.GetBytes(body, "unsigned_urls").ForEach(func(_, v gjson.Result) bool {
			if s := strings.TrimSpace(v.String()); s != "" {
				urls = append(urls, s)
			}
			return true
		})
		if len(urls) > 0 {
			return urls
		}
	}
	_ = taskID
	return nil
}
