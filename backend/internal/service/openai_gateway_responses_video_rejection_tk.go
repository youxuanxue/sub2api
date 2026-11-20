package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Responses video content parts are fail-closed at ingress. Chat Completions
// can rewrite video aliases for Volc; /v1/responses must not silently drop
// them (customer PDF: explicit 400 with these messages).
const (
	responsesInputVideoUnsupportedMessage = `responses content part "input_video" is not supported`
	responsesVideoUnsupportedMessage      = `unsupported responses content part "video"`
	responsesVideoURLUnsupportedMessage   = `responses content part "video_url" is not supported`
)

// ResponsesUnsupportedVideoPartMessage returns the client-facing 400 message
// when a Responses request carries a video content part or input item.
// Empty string means no video part was found.
func ResponsesUnsupportedVideoPartMessage(body []byte) string {
	input := gjson.GetBytes(body, "input")
	if !input.Exists() || input.Type == gjson.Null {
		return ""
	}
	if input.IsArray() {
		for _, item := range input.Array() {
			if msg := responsesUnsupportedVideoTypeMessage(item.Get("type").String()); msg != "" {
				return msg
			}
			if msg := responsesUnsupportedVideoInContent(item.Get("content")); msg != "" {
				return msg
			}
		}
		return ""
	}
	if input.IsObject() {
		if msg := responsesUnsupportedVideoTypeMessage(input.Get("type").String()); msg != "" {
			return msg
		}
		return responsesUnsupportedVideoInContent(input.Get("content"))
	}
	return ""
}

func responsesUnsupportedVideoInContent(content gjson.Result) string {
	if !content.Exists() || content.Type == gjson.Null {
		return ""
	}
	if content.IsArray() {
		for _, part := range content.Array() {
			if msg := responsesUnsupportedVideoTypeMessage(part.Get("type").String()); msg != "" {
				return msg
			}
		}
		return ""
	}
	if content.IsObject() {
		return responsesUnsupportedVideoTypeMessage(content.Get("type").String())
	}
	return ""
}

func responsesUnsupportedVideoTypeMessage(partType string) string {
	switch strings.ToLower(strings.TrimSpace(partType)) {
	case "input_video":
		return responsesInputVideoUnsupportedMessage
	case "video":
		return responsesVideoUnsupportedMessage
	case "video_url":
		return responsesVideoURLUnsupportedMessage
	default:
		return ""
	}
}
