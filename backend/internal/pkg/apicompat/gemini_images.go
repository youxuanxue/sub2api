package apicompat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// GeminiImageAspectRatios is the shared native generation vocabulary.
// Return a fresh slice so discovery callers cannot mutate the request contract.
func GeminiImageAspectRatios() []string {
	return []string{"1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}
}

// ImagesToGeminiGeneration is the shared request contract for admission and
// execution of the Antigravity Images facade. It never approximates a canvas
// size or drops provider options that the Gemini request cannot represent.
func ImagesToGeminiGeneration(body []byte) (string, []byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return "", nil, fmt.Errorf("request must be a JSON object")
	}
	for key := range fields {
		switch key {
		case "model", "prompt", "n", "size", "aspect_ratio", "response_format", "stream", "user":
		default:
			return "", nil, fmt.Errorf("unsupported Gemini Images parameter %q", key)
		}
	}
	stringField := func(key string) (string, error) {
		raw, ok := fields[key]
		if !ok {
			return "", nil
		}
		var value string
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
			return "", fmt.Errorf("%s must be a string", key)
		}
		return value, nil
	}
	values := map[string]string{}
	for _, key := range []string{"model", "prompt", "size", "aspect_ratio", "response_format", "user"} {
		value, err := stringField(key)
		if err != nil {
			return "", nil, err
		}
		values[key] = value
	}
	model := strings.TrimSpace(values["model"])
	if model == "" || strings.TrimSpace(values["prompt"]) == "" {
		return "", nil, fmt.Errorf("model and prompt are required")
	}
	if raw, ok := fields["n"]; ok {
		var n int
		if json.Unmarshal(raw, &n) != nil || n != 1 {
			return "", nil, fmt.Errorf("gemini images supports only n=1")
		}
	}
	if raw, ok := fields["stream"]; ok {
		var stream bool
		if string(raw) == "null" || json.Unmarshal(raw, &stream) != nil || stream {
			return "", nil, fmt.Errorf("gemini images supports only non-streaming requests")
		}
	}
	if f := values["response_format"]; f != "" && f != "b64_json" {
		return "", nil, fmt.Errorf("gemini images supports response_format=b64_json")
	}
	ratio := values["aspect_ratio"]
	if ratio == "" {
		ratio = "1:1"
	}
	if !slices.Contains(GeminiImageAspectRatios(), ratio) {
		return "", nil, fmt.Errorf("unsupported aspect_ratio")
	}
	size := values["size"]
	switch size {
	case "", "auto":
		size = "2K"
	case "1K", "2K", "4K":
	case "1024x1024", "2048x2048", "4096x4096":
		if ratio != "1:1" {
			return "", nil, fmt.Errorf("square size conflicts with aspect_ratio")
		}
		size = map[string]string{"1024x1024": "1K", "2048x2048": "2K", "4096x4096": "4K"}[size]
	default:
		return "", nil, fmt.Errorf("unsupported size; use 1K, 2K, 4K or a supported square canvas; use aspect_ratio for composition")
	}
	native, err := json.Marshal(map[string]any{
		"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": values["prompt"]}}}},
		"generationConfig": map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}, "imageConfig": map[string]any{"aspectRatio": ratio, "imageSize": size}},
	})
	return model, native, err
}

// GeminiGenerationToImages returns the OpenAI Images envelope, never Markdown
// or a data URL disguised as an HTTPS URL. The gateway already owns metering.
func GeminiGenerationToImages(body []byte) ([]byte, error) {
	var response struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					InlineData *struct {
						MIMEType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Candidates) != 1 || response.Candidates[0].FinishReason != "STOP" {
		return nil, fmt.Errorf("upstream did not complete image generation")
	}
	images := []map[string]string{}
	for _, part := range response.Candidates[0].Content.Parts {
		if part.InlineData == nil {
			continue
		}
		inline := part.InlineData
		switch inline.MIMEType {
		case "image/png", "image/jpeg", "image/webp":
		default:
			return nil, fmt.Errorf("unsupported upstream image MIME type")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(inline.Data)
		if err != nil || len(data) == 0 {
			return nil, fmt.Errorf("invalid upstream image encoding")
		}
		images = append(images, map[string]string{"b64_json": inline.Data})
	}
	if len(images) != 1 {
		return nil, fmt.Errorf("upstream did not return exactly one image")
	}
	return json.Marshal(map[string]any{"created": time.Now().Unix(), "data": images})
}
