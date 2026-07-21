package service

import "strings"

const (
	gatewayFailoverClientMessage       = "Upstream request could not be completed"
	legacyGatewayFailoverClientMessage = "All available accounts exhausted"
)

// GatewayFailoverClientMessage returns the client-facing message used when the
// gateway stops failover without completing the request. Stopping does not prove
// that every schedulable account was attempted: the loop may hit its switch
// budget or stop on a request-owned/non-retryable error.
func GatewayFailoverClientMessage(upstreamStatusCode int) string {
	return TkEnrichClaudeIncidentMessage(gatewayFailoverClientMessage, upstreamStatusCode)
}

// IsGatewayFailoverMessage identifies TokenKey's failover-terminal envelope.
// The legacy message remains accepted while prod and edge gateways may run
// different versions. Matching is exact after extracting the JSON message; the
// only accepted suffix is the incident context added by
// TkEnrichClaudeIncidentMessage.
func IsGatewayFailoverMessage(upstreamMsg string, responseBody []byte) bool {
	if isGatewayFailoverClientMessage(upstreamMsg) {
		return true
	}
	bodyMsg := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	if bodyMsg == "" {
		bodyMsg = strings.TrimSpace(string(responseBody))
	}
	return isGatewayFailoverClientMessage(bodyMsg)
}

func isGatewayFailoverClientMessage(message string) bool {
	message = strings.TrimSpace(message)
	for _, candidate := range []string{gatewayFailoverClientMessage, legacyGatewayFailoverClientMessage} {
		if strings.EqualFold(message, candidate) {
			return true
		}
		incidentPrefix := candidate + " (Anthropic upstream incident:"
		if strings.HasPrefix(strings.ToLower(message), strings.ToLower(incidentPrefix)) &&
			strings.Contains(message, claudeStatusPageURL) {
			return true
		}
	}
	return false
}
