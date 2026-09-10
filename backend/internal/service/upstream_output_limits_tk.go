package service

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Native Cursor and ChatGPT/Codex transports cannot enforce these limits.
// Call only at those transport boundaries; other supplies retain native limits.
var unsupportedOutputLimitFields = [...]string{"max_tokens", "max_output_tokens", "max_completion_tokens"}

func omitUnsupportedOutputLimits(body map[string]any) bool {
	changed := false
	for _, field := range unsupportedOutputLimitFields {
		if _, exists := body[field]; exists {
			delete(body, field)
			changed = true
		}
	}
	return changed
}

func omitUnsupportedOutputLimitsJSON(body []byte) ([]byte, bool, error) {
	normalized := body
	changed := false
	for _, field := range unsupportedOutputLimitFields {
		if !gjson.GetBytes(normalized, field).Exists() {
			continue
		}
		next, err := sjson.DeleteBytes(normalized, field)
		if err != nil {
			return body, false, fmt.Errorf("omit unsupported output limit %s: %w", field, err)
		}
		normalized = next
		changed = true
	}
	return normalized, changed, nil
}
