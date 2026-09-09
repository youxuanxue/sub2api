package service

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// tkNormalizeGeminiFunctionResponseImages converts the CLI's sibling image parts
// into Gemini 3 multimodal tool results when their tool ownership is unambiguous.
func tkNormalizeGeminiFunctionResponseImages(body []byte) []byte {
	if !gjson.ValidBytes(body) {
		return body
	}
	result := body
	for i, content := range gjson.GetBytes(body, "contents").Array() {
		if content.Get("role").String() != "user" {
			continue
		}
		parts := content.Get("parts").Array()
		responseIndex, responseCount := -1, 0
		images := make(map[int]bool)
		for j, part := range parts {
			if part.Get("functionResponse").IsObject() {
				responseIndex, responseCount = j, responseCount+1
			}
			if len(part.Map()) != 1 {
				continue
			}
			for _, field := range []string{"inlineData", "fileData"} {
				if strings.HasPrefix(part.Get(field+".mimeType").String(), "image/") {
					images[j] = true
				}
			}
		}
		if responseCount != 1 || len(images) == 0 {
			continue
		}
		existing := parts[responseIndex].Get("functionResponse.parts")
		if existing.Exists() && !existing.IsArray() {
			continue
		}
		media := make([]json.RawMessage, 0, len(existing.Array())+len(images))
		for _, part := range existing.Array() {
			media = append(media, json.RawMessage(part.Raw))
		}
		for j, part := range parts {
			if images[j] {
				media = append(media, json.RawMessage(part.Raw))
			}
		}
		mediaJSON, err := json.Marshal(media)
		if err != nil {
			return body
		}
		response, err := sjson.SetRaw(parts[responseIndex].Raw, "functionResponse.parts", string(mediaJSON))
		if err != nil {
			return body
		}
		updatedParts := make([]json.RawMessage, 0, len(parts)-len(images))
		for j, part := range parts {
			if j == responseIndex {
				updatedParts = append(updatedParts, json.RawMessage(response))
			} else if !images[j] {
				updatedParts = append(updatedParts, json.RawMessage(part.Raw))
			}
		}
		partsJSON, err := json.Marshal(updatedParts)
		if err != nil {
			return body
		}
		updated, err := sjson.SetRawBytes(result, "contents."+strconv.Itoa(i)+".parts", partsJSON)
		if err != nil {
			return body
		}
		result = updated
	}
	return result
}
