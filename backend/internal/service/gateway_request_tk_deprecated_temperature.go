package service

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// tkModelDeprecatesTemperature reports whether Anthropic rejects the top-level
// temperature field for this model. Live prod/edge traces on 2026-07-08 showed
// claude-opus-4-7 returning HTTP 400 "`temperature` is deprecated for this
// model."; Opus 4.7+ already has a distinct request surface elsewhere
// (adaptive-only thinking), so keep the gate on the same model predicate.
func tkModelDeprecatesTemperature(modelID string) bool {
	return isOpus47OrNewer(modelID)
}

// tkStripDeprecatedTemperature removes only the top-level temperature field for
// Anthropic models that now reject it. Nested JSON-schema properties named
// "temperature" must remain intact.
func tkStripDeprecatedTemperature(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	if !tkModelDeprecatesTemperature(model) || !gjson.GetBytes(body, "temperature").Exists() {
		return body
	}
	stripped, err := sjson.DeleteBytes(body, "temperature")
	if err != nil {
		return body
	}
	return stripped
}
