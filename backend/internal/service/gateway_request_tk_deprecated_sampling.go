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
// Anthropic's compatibility notes list temperature/top_p/top_k together for
// Opus 4.7+, Sonnet 4.6+, and Sonnet 5+.
func tkModelDeprecatesSamplingParams(modelID string) bool {
	return isOpus47OrNewer(modelID) ||
		tkClaudeFamilyMajorAtLeast(modelID, "opus", 5) ||
		tkClaudeFamilyVersionAtLeast(modelID, "sonnet", 4, 6) ||
		tkClaudeFamilyMajorAtLeast(modelID, "sonnet", 5)
}

func tkModelRejectsTemperatureTopPCombination(modelID string) bool {
	if tkModelDeprecatesSamplingParams(modelID) {
		return false
	}
	return tkClaudeFamilyVersionAtLeast(modelID, "opus", 4, 5) ||
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
	if len(body) == 0 {
		return body
	}
	model := gjson.GetBytes(body, "model").String()
	out := body

	if tkModelDeprecatesSamplingParams(model) {
		for _, field := range tkDeprecatedSamplingTopLevelFields {
			if !gjson.GetBytes(out, field).Exists() {
				continue
			}
			stripped, err := sjson.DeleteBytes(out, field)
			if err != nil {
				return body
			}
			out = stripped
		}
		return out
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
