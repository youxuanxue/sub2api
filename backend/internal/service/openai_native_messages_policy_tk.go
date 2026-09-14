package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Native Messages errors use the same policy owner as OpenAI wire errors.
// This bridge only renders the selected ingress protocol; it owns no retry,
// cooldown, session isolation or billing policy.
func forwardNativeMessagesPolicy(c *gin.Context, payload []byte, status int, usage *OpenAIUsage, protocol string, stream bool, responseID, model string) bool {
	kind := markOpenAISafetyPolicyEvent(c, payload, status, usage)
	if kind == "" {
		return false
	}
	message := extractUpstreamErrorMessage(payload)
	if message == "" {
		message = "Request blocked by upstream usage policy"
	}
	// Header-wait keepalive can commit SSE before an upstream HTTP rejection.
	// Continue that wire format even though the upstream itself never streamed.
	stream = stream || (c.Writer.Written() && strings.HasPrefix(strings.ToLower(c.Writer.Header().Get("Content-Type")), "text/event-stream"))
	if stream {
		switch protocol {
		case "messages":
			canonical, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "code": kind, "message": message}})
			_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", canonical)
		case "chat":
			_, _ = fmt.Fprint(c.Writer, buildChatStreamErrorSSE(kind, message), "data: [DONE]\n\n")
		case "responses":
			canonical, _ := json.Marshal(gin.H{"error": gin.H{"type": "invalid_request_error", "code": kind, "message": message}})
			_, _ = fmt.Fprint(c.Writer, buildOpenAIResponseFailedSSE(responseID, model, canonical, message))
		}
		c.Writer.Flush()
	} else {
		status = mapUpstreamStatusCode(status)
		if status < 400 {
			status = http.StatusBadRequest
		}
		switch protocol {
		case "messages":
			c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": "invalid_request_error", "code": kind, "message": message}})
		case "responses":
			writeResponsesError(c, status, kind, message)
		case "chat":
			c.JSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "code": kind, "message": message}})
		}
	}
	MarkResponseCommitted(c)
	return true
}
