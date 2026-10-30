package service

import (
	"encoding/json"
	"math"
	"strconv"
)

// anthropicDeltaInputPatch owns cumulative message_delta input corrections for
// both native parsers. An explicit canonical counter outranks compatibility
// aliases, including zero. previous fences invalid fields from later alias
// normalization in the passthrough parser; the standard parser needs no fence.
func anthropicDeltaInputPatch(fields map[string]any, previous *ClaudeUsage) *sseUsagePatch {
	patch := &sseUsagePatch{}
	for _, field := range []struct {
		name     string
		value    *int
		present  *bool
		oldValue func(*ClaudeUsage) int
	}{
		{"input_tokens", &patch.inputTokens, &patch.hasInputTokens, func(u *ClaudeUsage) int { return u.InputTokens }},
		{"cache_creation_input_tokens", &patch.cacheCreationInputTokens, &patch.hasCacheCreationInput, func(u *ClaudeUsage) int { return u.CacheCreationInputTokens }},
		{"cache_read_input_tokens", &patch.cacheReadInputTokens, &patch.hasCacheReadInput, func(u *ClaudeUsage) int { return u.CacheReadInputTokens }},
	} {
		raw, exists := fields[field.name]
		if !exists {
			continue
		}
		value, valid := parseNonNegativeSSEUsageInt(raw)
		if valid {
			*field.value, *field.present = value, true
			if field.name == "cache_creation_input_tokens" && value == 0 {
				// A definitive zero cannot retain stale or contradictory TTL
				// details: billing otherwise charges tokens that were cleared.
				patch.hasCacheCreation5m, patch.hasCacheCreation1h = true, true
			}
		} else if previous != nil {
			*field.value, *field.present = field.oldValue(previous), true
		}
	}
	return patch
}

func parseNonNegativeSSEUsageInt(raw any) (int, bool) {
	// Token counters must be integers. Validate before conversion so either
	// -0.5 or 0.5 cannot become an authoritative zero and clear valid usage.
	var number float64
	switch value := raw.(type) {
	case float64:
		number = value
	case float32:
		number = float64(value)
	case json.Number:
		parsed, err := strconv.ParseFloat(string(value), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		parsed, ok := parseSSEUsageInt(raw)
		return parsed, ok && parsed >= 0
	}
	if number < 0 || number != math.Trunc(number) || math.IsNaN(number) || math.IsInf(number, 0) || number >= math.Ldexp(1, strconv.IntSize-1) {
		return 0, false
	}
	return int(number), true
}
