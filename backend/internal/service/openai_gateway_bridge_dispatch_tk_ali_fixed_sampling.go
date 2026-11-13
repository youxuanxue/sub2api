package service

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyNewAPIAliFixedSamplingShape prepares kimi-k3 sampling before the new-api
// Ali adaptor runs.
//
// new-api relay/channel/ali.requestOpenAI2Ali rewrites missing/zero top_p to
// 0.001. kimi-k3 accepts only top_p 0.95, so an omitted top_p is still written
// as 0.95. A client temperature that rejectKimiK3FixedSampling accepts may
// still be pinned to 1.0 for DashScope.
//
// Values that decision rejects (temperature 1.1 or 2.0, any other top_p,
// penalties other than 0, n other than 1, and out-of-range or non-numeric
// sampling fields) are left unchanged. applyMoonshotThinkingShape then returns
// 400. Rewriting them to a legal value here made the bridge answer 200.
func applyNewAPIAliFixedSamplingShape(model string, body []byte) []byte {
	if len(body) == 0 || !isNewAPIAliFixedSamplingModel(model) {
		return body
	}
	shaped := body
	if !volcKimiFieldSet(gjson.GetBytes(shaped, "top_p")) {
		if next, err := sjson.SetBytes(shaped, "top_p", 0.95); err == nil {
			shaped = next
		}
	}
	if temp := gjson.GetBytes(shaped, "temperature"); volcKimiFieldSet(temp) && kimiK3FixedTemperatureAllowed(temp) {
		if next, err := sjson.SetBytes(shaped, "temperature", 1.0); err == nil {
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
	// kimi-k3.1-pro is the same public K3 family for tools and Volc limits, but
	// this pin rewrites temperature to 1. Leave dotted ids out so a client
	// temperature is still forwarded on the Volc path.
	return normalized == "kimi-k3" || strings.HasPrefix(normalized, "kimi-k3-")
}
