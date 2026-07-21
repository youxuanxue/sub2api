package handler

import (
	"net/http"
	"strings"
)

func tkOpsClassifyFinalClientValidationPhase(phase string, upstreamError, routingCapacityLimited bool, errType, message, code string, status int) string {
	if phase != "internal" || upstreamError || routingCapacityLimited {
		return phase
	}
	if tkOpsFinalClientValidationAPIError(errType, message, code, status) {
		return "request"
	}
	return phase
}

// tkOpsFinalClientValidationAPIError catches client request-validation errors
// that were flattened into a final api_error before ops logging sees them.
// Upstream-context errors ride tkUpstreamClientInducedRejection instead.
func tkOpsFinalClientValidationAPIError(errType, message, code string, status int) bool {
	if strings.TrimSpace(strings.ToLower(errType)) != "api_error" {
		return false
	}
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
	default:
		return false
	}

	msg := strings.ToLower(strings.TrimSpace(message))
	code = strings.ToLower(strings.TrimSpace(code))
	if msg == "" && code == "" {
		return false
	}
	if tkOpsIsAccountLevel4xx(msg) {
		return false
	}
	if tkOpsClientValidationCode(code) {
		return true
	}
	if status == http.StatusRequestEntityTooLarge && tkOpsRequestSizeValidationMessage(msg) {
		return true
	}
	return tkOpsRequestParameterValidationMessage(msg)
}

func tkOpsClientValidationCode(code string) bool {
	switch code {
	case "bad_request",
		"content_filter",
		"invalid_request",
		"invalid_request_error",
		"invalid_parameter",
		"invalid_value",
		"missing_required_parameter",
		"parameter_missing",
		"unknown_parameter",
		"unsupported_parameter",
		"request_too_large":
		return true
	default:
		return false
	}
}

func tkOpsRequestParameterValidationMessage(lowerMessage string) bool {
	if lowerMessage == "" {
		return false
	}
	compact := strings.NewReplacer("`", "", "'", "", "\"", "", " ", "").Replace(lowerMessage)
	if strings.Contains(compact, "temperatureandtop_pcannotbothbespecified") {
		return true
	}
	if strings.Contains(lowerMessage, "extra inputs are not permitted") ||
		strings.Contains(lowerMessage, "unsupported parameter") ||
		strings.Contains(lowerMessage, "unknown parameter") ||
		strings.Contains(lowerMessage, "unknown_parameter") ||
		strings.Contains(lowerMessage, "unrecognized request argument") ||
		strings.Contains(lowerMessage, "missing required parameter") ||
		strings.Contains(lowerMessage, "required parameter") ||
		strings.Contains(lowerMessage, "invalid request body") ||
		strings.Contains(lowerMessage, "failed to parse request body") {
		return true
	}

	if strings.Contains(lowerMessage, "invalid value") &&
		(strings.Contains(lowerMessage, "parameter") || strings.Contains(lowerMessage, "param") || tkOpsMessageMentionsRequestField(lowerMessage)) {
		return true
	}

	if !tkOpsMessageMentionsRequestField(lowerMessage) {
		return false
	}
	return strings.Contains(lowerMessage, "cannot both be specified") ||
		strings.Contains(lowerMessage, "cannot be specified") ||
		strings.Contains(lowerMessage, "not supported") ||
		strings.Contains(lowerMessage, "not permitted") ||
		strings.Contains(lowerMessage, "not allowed") ||
		strings.Contains(lowerMessage, "deprecated") ||
		strings.Contains(lowerMessage, "non-default") ||
		strings.Contains(lowerMessage, "input should be") ||
		strings.Contains(lowerMessage, "must be") ||
		strings.Contains(lowerMessage, "expected")
}

func tkOpsRequestSizeValidationMessage(lowerMessage string) bool {
	return (strings.Contains(lowerMessage, "request") ||
		strings.Contains(lowerMessage, "body") ||
		strings.Contains(lowerMessage, "payload") ||
		strings.Contains(lowerMessage, "prompt")) &&
		(strings.Contains(lowerMessage, "too large") ||
			strings.Contains(lowerMessage, "too long") ||
			strings.Contains(lowerMessage, "exceed"))
}

func tkOpsMessageMentionsRequestField(lowerMessage string) bool {
	if strings.Contains(lowerMessage, "parameter") ||
		strings.Contains(lowerMessage, "param") ||
		strings.Contains(lowerMessage, "field") ||
		strings.Contains(lowerMessage, "argument") {
		return true
	}
	for _, field := range []string{
		"temperature",
		"top_p",
		"top_k",
		"max_tokens",
		"max_output_tokens",
		"max_input_tokens",
		"max_completion_tokens",
		"stop_sequences",
		"service_tier",
		"metadata",
		"stream",
		"model",
		"messages",
		"system",
		"input",
		"instructions",
		"thinking",
		"budget_tokens",
		"output_config",
		"effort",
		"tools",
		"tool_choice",
		"response_format",
		"parallel_tool_calls",
		"context_management",
		"betas",
		"enable_thinking",
		"prompt_cache_key",
	} {
		if strings.Contains(lowerMessage, field) {
			return true
		}
	}
	return false
}
