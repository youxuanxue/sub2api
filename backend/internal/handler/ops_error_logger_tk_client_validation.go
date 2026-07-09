package handler

import (
	"net/http"
	"strings"
)

func tkOpsClassifyFinalClientValidationPhase(phase string, upstreamError, routingCapacityLimited bool, errType, message string, status int) string {
	if phase != "internal" || upstreamError || routingCapacityLimited {
		return phase
	}
	if tkOpsFinalClientValidationAPIError(errType, message, status) {
		return "request"
	}
	return phase
}

// tkOpsFinalClientValidationAPIError catches client request-validation errors
// that were already flattened into a final Anthropic/OpenAI-style api_error
// response before ops logging sees them. These have no upstream context, so they
// cannot ride tkUpstreamClientInducedRejection, but they are still caller-fault.
func tkOpsFinalClientValidationAPIError(errType, message string, status int) bool {
	if status != http.StatusBadRequest || strings.TrimSpace(errType) != "api_error" {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	return tkOpsAnthropicSamplingParamConflict(msg)
}

func tkOpsAnthropicSamplingParamConflict(lowerMessage string) bool {
	compact := strings.NewReplacer("`", "", "'", "", "\"", "", " ", "").Replace(lowerMessage)
	return strings.Contains(compact, "temperatureandtop_pcannotbothbespecified") ||
		(strings.Contains(compact, "temperature") &&
			strings.Contains(compact, "top_p") &&
			strings.Contains(compact, "cannotbothbespecified"))
}
