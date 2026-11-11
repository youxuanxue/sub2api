package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PrepareSeedanceVideoRequest adapts Ark content to the existing video owner.
// Keep the original content so shared moderation and unpriced-input guards see
// the same inputs. Raw Ark accounts do not use this restricted adapter.
func PrepareSeedanceVideoRequest(body []byte) ([]byte, error) {
	info, err := ParseSeedanceRequest(body)
	if err != nil {
		return nil, err
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	var content []struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(request["content"], &content); err != nil {
		return nil, fmt.Errorf("invalid Seedance content")
	}
	for _, part := range content {
		switch part.Type {
		case "text":
		case "image_url":
			if part.Role != "" && part.Role != "first_frame" {
				return nil, fmt.Errorf("this Seedance provider supports only first-frame image input")
			}
		default:
			return nil, fmt.Errorf("unsupported Seedance content type %q; only text and first-frame image input are supported", part.Type)
		}
	}
	if len(info.InputImageURLs) > 1 {
		return nil, fmt.Errorf("this Seedance provider supports at most one first-frame image")
	}
	if strings.TrimSpace(info.Prompt) == "" {
		return nil, fmt.Errorf("text content is required")
	}
	request["prompt"], _ = json.Marshal(info.Prompt)
	// Disallow conflicting generic image fields: billing, moderation and the
	// adaptor must all consume exactly the image from Ark content.
	for _, field := range []string{"images", "image", "input_reference"} {
		if _, exists := request[field]; exists {
			return nil, fmt.Errorf("use content.image_url instead of %s", field)
		}
	}
	if len(info.InputImageURLs) == 1 {
		if strings.TrimSpace(info.InputImageURLs[0]) == "" {
			return nil, fmt.Errorf("image_url.url is required")
		}
		request["image"], _ = json.Marshal(info.InputImageURLs[0])
	}
	return json.Marshal(request)
}
