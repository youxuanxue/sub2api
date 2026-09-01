package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyNewAPIAliFixedSamplingShape pins DashScope/Moonshot fixed sampling params
// for kimi-k3 before the new-api Ali adaptor runs.
//
// new-api relay/channel/ali.requestOpenAI2Ali rewrites missing/zero top_p to
// 0.001. kimi-k3 rejects any top_p other than the server-fixed 0.95 (omit is
// OK to DashScope directly, but the adaptor always injects a value). Live
// 2026-09-01 probe: top_p=0.95 and temperature=1.0 succeed; 0.001/0.8/0.7 fail.
func applyNewAPIAliFixedSamplingShape(model string, body []byte) []byte {
	if len(body) == 0 || !isNewAPIAliFixedSamplingModel(model) {
		return body
	}
	shaped := body
	if next, err := sjson.SetBytes(shaped, "top_p", 0.95); err == nil {
		shaped = next
	}
	if gjson.GetBytes(shaped, "temperature").Exists() {
		if next, err := sjson.SetBytes(shaped, "temperature", 1.0); err == nil {
			shaped = next
		}
	}
	if gjson.GetBytes(shaped, "n").Exists() {
		if next, err := sjson.SetBytes(shaped, "n", 1); err == nil {
			shaped = next
		}
	}
	if gjson.GetBytes(shaped, "presence_penalty").Exists() {
		if next, err := sjson.SetBytes(shaped, "presence_penalty", 0); err == nil {
			shaped = next
		}
	}
	if gjson.GetBytes(shaped, "frequency_penalty").Exists() {
		if next, err := sjson.SetBytes(shaped, "frequency_penalty", 0); err == nil {
			shaped = next
		}
	}
	return shaped
}

func isNewAPIAliFixedSamplingModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	// Drop optional vendor prefix used by some DashScope listings (kimi/kimi-k3).
	if idx := strings.LastIndex(normalized, "/"); idx >= 0 {
		normalized = normalized[idx+1:]
	}
	return normalized == "kimi-k3" || strings.HasPrefix(normalized, "kimi-k3-")
}
