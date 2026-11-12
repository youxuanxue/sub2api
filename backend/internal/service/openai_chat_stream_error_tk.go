package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type chatStreamInBandError struct {
	payload []byte
}

func (e *chatStreamInBandError) Error() string { return "upstream chat stream returned an error" }

func detectChatStreamError(payload string) *chatStreamInBandError {
	if !gjson.Valid(payload) {
		return nil
	}
	value := gjson.Get(payload, "error")
	if !value.Exists() || value.Type == gjson.Null {
		return nil
	}
	safe, _ := sanitizeOpenAIResponseFailedEventForClient([]byte(payload), "error", false)
	// Classification consumes the sanitized envelope too. Keep the recognized
	// numeric status internally without exposing arbitrary upstream diagnostics.
	for _, path := range openAIStreamErrorStatusPaths {
		status := int(gjson.Get(payload, path).Int())
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests || status == 529 {
			safe, _ = sjson.SetBytes(safe, "error.status_code", status)
			break
		}
	}
	return &chatStreamInBandError{payload: safe}
}

func chatPayloadHasBusinessOutput(payload string) bool {
	for _, choice := range gjson.Get(payload, "choices").Array() {
		delta := choice.Get("delta")
		if delta.Get("content").String() != "" || delta.Get("refusal").String() != "" || delta.Get("reasoning_content").String() != "" || len(delta.Get("tool_calls").Array()) > 0 || delta.Get("function_call").IsObject() {
			return true
		}
	}
	return false
}

// Reuse Responses' semantic classifier and attempt budget, not HTTP 200 or
// metadata arrival, to decide whether a Chat stream can be retried.
func (s *OpenAIGatewayService) handleChatStreamFailure(c *gin.Context, resp *http.Response, account *Account, payload []byte, outputStarted bool, model string) error {
	message := extractOpenAISSEErrorMessage(payload)
	if message == "" {
		message = "Upstream chat stream failed"
	}
	if !outputStarted && openAIStreamFailedEventShouldFailover(payload, message) {
		if account != nil {
			return s.newOpenAIStreamFailoverErrorWithModel(c, account, false, resp.Header.Get("x-request-id"), payload, message, model, resp.Header)
		}
		return &UpstreamFailoverError{StatusCode: openAIStreamFailedEventSemanticStatus(payload, message), Scope: GatewayFailureScopeAccount, ClientStatusCode: http.StatusBadGateway, ClientErrorType: "upstream_error", ClientMessage: message}
	}
	if account != nil {
		message = s.recordOpenAIStreamUpstreamError(c, account, false, resp.Header.Get("x-request-id"), "http_error", payload, message)
	} else {
		setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	}
	return fmt.Errorf("upstream chat stream failed: %s", strings.TrimSpace(message))
}
