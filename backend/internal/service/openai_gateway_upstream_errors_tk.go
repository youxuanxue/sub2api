package service

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// tkShouldPassthroughOpenAINativeClientError decides whether handleErrorResponse
// should return the upstream status to the end user instead of generic 502.
//
// Runs AFTER the failover decision: pool retry already returned when
// handleOpenAIAccountUpstreamError requested it. Terminal passthrough here
// improves TokenKey end-user DX without blocking account-pool failover.
//
//   - 422: always caller-fault (schema/validation).
//   - 404: only model-not-found shapes; Unknown URL / base_url misconfig stays 502.
//   - 400: owned by upstream writeOpenAIUpstreamClientError (#5479), not here.
func tkShouldPassthroughOpenAINativeClientError(statusCode int, upstreamMsg string, body []byte) bool {
	switch statusCode {
	case http.StatusUnprocessableEntity:
		return true
	case http.StatusNotFound:
		return isUpstreamModelNotFoundError(statusCode, body) ||
			IsOpenAICompatModelNotFound404(body, upstreamMsg)
	default:
		return false
	}
}

// tkWriteOpenAINativeClientError mirrors writeOpenAIUpstreamClientError but sets
// OpenAI-native default types for 404/422 while preserving upstream code/param.
func tkWriteOpenAINativeClientError(c *gin.Context, statusCode int, body []byte, upstreamMsg string) {
	if statusCode == http.StatusBadRequest {
		writeOpenAIUpstreamClientError(c, statusCode, body, upstreamMsg)
		return
	}

	defaultType := openAIUpstreamClientErrorFallbackType
	switch statusCode {
	case http.StatusNotFound:
		defaultType = "not_found_error"
	case http.StatusUnprocessableEntity:
		defaultType = "invalid_request_error"
	}

	errorPayload := gin.H{"type": defaultType}
	if errType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String()); errType != "" {
		errorPayload["type"] = errType
	}
	if code := strings.TrimSpace(extractUpstreamErrorCode(body)); code != "" {
		errorPayload["code"] = code
	}
	if param := strings.TrimSpace(gjson.GetBytes(body, "error.param").String()); param != "" {
		errorPayload["param"] = param
	}
	message := strings.TrimSpace(upstreamMsg)
	if message == "" {
		message = openAIUpstreamClientErrorFallbackMessage
	}
	errorPayload["message"] = message

	c.JSON(statusCode, gin.H{"error": errorPayload})
}
