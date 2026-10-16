package kiro

import (
	"encoding/json"
	"strings"
)

// IsUnsupportedImageFormatError recognizes the request-level rejection emitted
// without an AWS exception type. Only the error message is evidence; echoed
// request content must not suppress failover for an unrelated provider error.
func IsUnsupportedImageFormatError(body []byte) bool {
	var payload struct {
		Message string `json:"message"`
	}
	return json.Unmarshal(body, &payload) == nil &&
		strings.EqualFold(strings.TrimSpace(payload.Message), "Image format is not supported.")
}
