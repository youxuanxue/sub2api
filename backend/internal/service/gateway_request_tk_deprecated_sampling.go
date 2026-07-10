package service

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var tkDeprecatedSamplingTopLevelFields = []string{
	"temperature",
	"top_p",
	"top_k",
}

// tkModelDeprecatesSamplingParams reports whether Anthropic rejects non-default
// top-level sampling params for this model. Live prod/edge traces on 2026-07-08
// showed claude-opus-4-7 returning HTTP 400 for both `temperature` and `top_p`;
// Anthropic's compatibility notes list temperature/top_p/top_k as removed for
// Opus 4.7+ and Sonnet 5+. Sonnet 4.6 keeps the older temperature+top_p
// mutual-exclusion behavior handled below.
func tkModelDeprecatesSamplingParams(modelID string) bool {
	return isOpus47OrNewer(modelID) ||
		tkClaudeFamilyMajorAtLeast(modelID, "opus", 5) ||
		tkClaudeFamilyMajorAtLeast(modelID, "sonnet", 5)
}

func tkModelRejectsTemperatureTopPCombination(modelID string) bool {
	if tkModelDeprecatesSamplingParams(modelID) {
		return false
	}
	return tkClaudeFamilyVersionAtLeast(modelID, "opus", 4, 1) ||
		tkClaudeFamilyVersionAtLeast(modelID, "sonnet", 4, 5) ||
		tkClaudeFamilyVersionAtLeast(modelID, "haiku", 4, 5)
}

func tkClaudeFamilyMajorAtLeast(modelID, family string, minMajor int) bool {
	lower := strings.ToLower(modelID)
	if !strings.Contains(lower, family) {
		return false
	}
	matches := claudeVersionRe.FindStringSubmatch(lower)
	if matches != nil {
		major, _ := strconv.Atoi(matches[1])
		return major >= minMajor
	}

	marker := "claude-" + family + "-"
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return false
	}
	tail := lower[idx+len(marker):]
	end := 0
	for end < len(tail) && tail[end] >= '0' && tail[end] <= '9' {
		end++
	}
	if end == 0 {
		return false
	}
	major, err := strconv.Atoi(tail[:end])
	return err == nil && major >= minMajor
}

func tkClaudeFamilyVersionAtLeast(modelID, family string, minMajor, minMinor int) bool {
	lower := strings.ToLower(modelID)
	if !strings.Contains(lower, family) {
		return false
	}
	matches := claudeVersionRe.FindStringSubmatch(lower)
	if matches == nil {
		return false
	}
	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	return major > minMajor || (major == minMajor && minor >= minMinor)
}

// tkStripDeprecatedSamplingParams removes only top-level sampling fields for
// Anthropic models that now reject them, and removes top_p for current Claude
// models that reject temperature+top_p in the same request. Nested JSON-schema
// properties with the same names must remain intact.
func tkStripDeprecatedSamplingParams(body []byte) []byte {
	return tkStripDeprecatedSamplingParamsForAccount(nil, body)
}

func tkStripDeprecatedSamplingParamsForAccount(account *Account, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	out := body

	if tkModelDeprecatesSamplingParams(model) {
		return tkStripTopLevelSamplingFields(out, body)
	}

	if cachedRule, ok := tkGetCachedSamplingParamRule(account, model, body); ok {
		switch cachedRule {
		case tkSamplingParamRuleStripTopPWithTemperature:
			if gjson.GetBytes(out, "temperature").Exists() && gjson.GetBytes(out, "top_p").Exists() {
				stripped, err := sjson.DeleteBytes(out, "top_p")
				if err != nil {
					return body
				}
				out = stripped
			}
			return out
		case tkSamplingParamRuleStripTemperature:
			return tkStripTopLevelSamplingField(out, body, "temperature")
		case tkSamplingParamRuleStripTopP:
			return tkStripTopLevelSamplingField(out, body, "top_p")
		case tkSamplingParamRuleStripTopK:
			return tkStripTopLevelSamplingField(out, body, "top_k")
		}
	}

	if tkModelRejectsTemperatureTopPCombination(model) &&
		gjson.GetBytes(out, "temperature").Exists() &&
		gjson.GetBytes(out, "top_p").Exists() {
		stripped, err := sjson.DeleteBytes(out, "top_p")
		if err != nil {
			return body
		}
		out = stripped
	}
	return out
}

func tkStripTopLevelSamplingField(body []byte, fallback []byte, field string) []byte {
	if !gjson.GetBytes(body, field).Exists() {
		return body
	}
	stripped, err := sjson.DeleteBytes(body, field)
	if err != nil {
		return fallback
	}
	return stripped
}

func tkStripTopLevelSamplingFields(body []byte, fallback []byte) []byte {
	out := body
	for _, field := range tkDeprecatedSamplingTopLevelFields {
		if !gjson.GetBytes(out, field).Exists() {
			continue
		}
		stripped, err := sjson.DeleteBytes(out, field)
		if err != nil {
			return fallback
		}
		out = stripped
	}
	return out
}
