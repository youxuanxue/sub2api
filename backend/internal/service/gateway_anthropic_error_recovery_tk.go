package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Match only the observed supplier sentence and diagnostic IDs. A broad
// "too long"/Chinese substring match could turn unrelated validation into
// destructive client-side history recovery. Never invent a token count.
var tkAnthropicContextLimitMessage = regexp.MustCompile(`^输入内容过长，请减少 Prompt 中的 token 数量后重试(?: \((?:cch_session_id|request id): [A-Za-z0-9-]+\))*$`)

func tkWriteAnthropicError(c *gin.Context, statusCode int, errType, message string) {
	tkWriteAnthropicErrorResponse(c, statusCode, errType, message, nil)
}

// Native Anthropic preserves additional upstream 400 fields. Normalize only
// the observed context rejection; retain other JSON envelopes byte-for-byte
// until SSE framing requires compact JSON.
func tkWriteAnthropicPassthrough400(c *gin.Context, body []byte) {
	message := sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(body))
	errType := gjson.GetBytes(body, "error.type").String()
	if (errType == "invalid_request_error" || errType == "<nil>") && tkAnthropicContextLimitMessage.MatchString(message) {
		tkWriteAnthropicError(c, http.StatusBadRequest, "invalid_request_error", message)
		return
	}
	tkWriteAnthropicErrorResponse(c, http.StatusBadRequest, errType, message, body)
}

func tkWriteAnthropicErrorResponse(c *gin.Context, statusCode int, errType, message string, payload []byte) {
	if statusCode == http.StatusBadRequest && errType == "invalid_request_error" && tkAnthropicContextLimitMessage.MatchString(message) {
		message = "prompt is too long: " + message
	}
	if payload == nil {
		payload, _ = json.Marshal(gin.H{"type": "error", "error": gin.H{"type": errType, "message": message}})
	}
	MarkResponseCommitted(c)
	// A header-wait heartbeat has already committed HTTP 200. Changing that
	// status and appending JSON would hide the failure from both SSE clients and
	// Ops. The stream marker snapshots the original upstream diagnostic context.
	if c.Writer.Written() && strings.HasPrefix(strings.ToLower(c.Writer.Header().Get("Content-Type")), "text/event-stream") {
		if errType == "" || errType == "<nil>" {
			errType = "invalid_request_error"
		}
		MarkOpsStreamFailure(c, errType, "", message, statusCode)
		// A native 400 may omit the envelope type or contain a non-JSON body.
		// Preserve valid error fields while producing a consumable SSE event.
		if gjson.ValidBytes(payload) && gjson.GetBytes(payload, "error").IsObject() {
			payload, _ = sjson.SetBytes(payload, "type", "error")
			payload, _ = sjson.SetBytes(payload, "error.type", errType)
		} else {
			_, _ = fmt.Fprint(c.Writer, buildAnthropicStreamErrorSSE(errType, message))
			c.Writer.Flush()
			return
		}
		var compact bytes.Buffer
		if json.Compact(&compact, payload) != nil {
			// An invalid supplier body cannot become SSE data with embedded newlines.
			_, _ = fmt.Fprint(c.Writer, buildAnthropicStreamErrorSSE(errType, message))
		} else {
			_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", compact.Bytes())
		}
		c.Writer.Flush()
		return
	}
	c.Header("Content-Type", "application/json")
	c.Data(statusCode, "application/json", payload)
}
