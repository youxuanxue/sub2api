package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// IsUpstreamModelRetiredError requires a model retirement diagnostic. Gone alone
// may describe an expired file/session; echoed request data is not a diagnostic.
func IsUpstreamModelRetiredError(statusCode int, body []byte, message ...string) bool {
	return isUpstreamModelRetiredError(statusCode, body, message...)
}

func isUpstreamModelRetiredError(statusCode int, body []byte, message ...string) bool {
	switch statusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusGone:
	default:
		return false
	}
	match := func(text string) bool {
		normalized := normalizeModelNotFoundBody([]byte(text))
		if !strings.Contains(normalized, "model") || strings.Contains(normalized, "temporar") {
			return false
		}
		return strings.Contains(normalized, "end of life") || strings.Contains(normalized, "retired") ||
			(statusCode == http.StatusGone && strings.Contains(normalized, "no longer available"))
	}
	for _, text := range message {
		if match(text) {
			return true
		}
	}
	if !gjson.ValidBytes(body) {
		return match(string(body))
	}
	for _, path := range []string{"error.message", "response.error.message", "message", "detail", "error"} {
		value := gjson.GetBytes(body, path)
		if value.Type == gjson.String && match(value.String()) {
			return true
		}
	}
	return false
}
