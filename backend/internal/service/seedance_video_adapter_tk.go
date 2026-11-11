package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
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
		images, err := json.Marshal([]string{strings.TrimSpace(info.InputImageURLs[0])})
		if err != nil {
			return nil, err
		}
		request["images"] = images
	}
	if err := projectSeedanceFieldsForDoubao(request); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}

// projectSeedanceFieldsForDoubao copies Ark fields onto the carriers
// task/doubao convertToRequestPayload already reads: Images, Seconds, and
// Metadata. A singular image, or a top-level duration and ratio, never arrive
// there. Top-level content, duration, and ratio stay for billing.
func projectSeedanceFieldsForDoubao(request map[string]json.RawMessage) error {
	meta := map[string]json.RawMessage{}
	if raw, ok := request["metadata"]; ok {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return fmt.Errorf("metadata must be an object")
		}
	}
	if content, ok := request["content"]; ok {
		if _, exists := meta["content"]; !exists {
			meta["content"] = content
		}
	}
	if raw, ok := request["duration"]; ok {
		if _, exists := request["seconds"]; !exists {
			if seconds, ok := seedanceSecondsLiteral(raw); ok {
				encoded, err := json.Marshal(seconds)
				if err != nil {
					return err
				}
				request["seconds"] = encoded
			}
		}
		if _, exists := meta["duration"]; !exists {
			meta["duration"] = raw
		}
	}
	if raw, ok := request["ratio"]; ok {
		if _, exists := meta["ratio"]; !exists {
			meta["ratio"] = raw
		}
	}
	if len(meta) == 0 {
		return nil
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	request["metadata"] = encoded
	return nil
}

func seedanceSecondsLiteral(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", false
		}
		text = strings.TrimSpace(text)
		return text, text != ""
	}
	var whole int
	if err := json.Unmarshal(raw, &whole); err == nil && whole > 0 {
		return strconv.Itoa(whole), true
	}
	return "", false
}
