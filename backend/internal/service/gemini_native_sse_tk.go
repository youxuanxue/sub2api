package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
)

// Both legacy comment heartbeats and whitespace from updated Edges carry
// liveness. Translate them to whitespace instead of exposing SSE fields that
// @google/genai cannot consume. Event/id/retry fields carry no heartbeat.
func isGeminiNativeSSEKeepalive(line string) bool {
	line = strings.TrimSpace(line)
	return line == "" || strings.HasPrefix(line, ":")
}

// Native Gemini errors use a complete data frame, without an event: prefix.
// Do not emit a successful finishReason or [DONE] on a failed stream.
func geminiNativeSSEErrorFrame(status int, message string) string {
	payload, _ := json.Marshal(googleapi.ErrorResponse{Error: googleapi.ErrorDetail{
		Code: status, Message: message, Status: googleapi.HTTPStatusToGoogleStatus(status),
	}})
	return fmt.Sprintf("data: %s\n\n", payload)
}
