package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

func shouldFailoverOpenAIPassthroughResponse(account *Account, statusCode int, responseBody []byte) bool {
	semantic := gatewayFailureSemanticUnclassified
	if hit, _, _ := detectOpenAICyberPolicy(responseBody); hit {
		semantic = gatewayFailureSemanticSharedFault
	} else if isOpenAIContextWindowError("", responseBody) {
		semantic = gatewayFailureSemanticSharedFault
	} else if isOpenAIHTTPUpstreamAccessStateError(statusCode, "", responseBody) ||
		isOpenAIRequestBodyTooLargeError(statusCode, "", responseBody) {
		semantic = gatewayFailureSemanticAccountFault
	}
	obs := gatewayFailoverObservation{
		Profile:    gatewayFailoverProfileOpenAIPassthrough,
		Semantic:   semantic,
		StatusCode: statusCode,
	}
	if account != nil {
		obs.AccountType = account.Type
		obs.AccountConfiguredRetry = account.IsPoolMode() && account.IsPoolModeRetryableStatus(statusCode)
	}
	return classifyGatewayFailover(obs).RetryNextAccount
}

// openAIStreamFailedClientResponse maps response.failed to the downstream HTTP
// status/type when no admin passthrough rule matches. Caller-fault rejections
// must not surface as 502 because clients retry that status indefinitely.
func openAIStreamFailedClientResponse(payload []byte, message string, default5xxErrType string) (statusCode int, errType string) {
	statusCode = openAIStreamFailedEventSemanticStatus(payload, message)
	switch statusCode {
	case http.StatusBadRequest:
		errType = "invalid_request_error"
	case http.StatusUnauthorized:
		errType = "authentication_error"
	case http.StatusForbidden:
		errType = "permission_error"
	case http.StatusTooManyRequests:
		errType = "rate_limit_error"
	case http.StatusServiceUnavailable:
		errType = "api_error"
	default:
		if statusCode >= 500 {
			errType = default5xxErrType
		} else {
			errType = "api_error"
		}
	}
	return statusCode, errType
}

func openAIStreamFailedEventShouldFailover(payload []byte, message string) bool {
	semantic := gatewayFailureSemanticTransientFault
	if hit, _, _ := detectOpenAICyberPolicy(payload); hit {
		semantic = gatewayFailureSemanticSharedFault
	} else if isOpenAIContextWindowError(message, payload) {
		semantic = gatewayFailureSemanticSharedFault
	} else if isOpenAIUpstreamAccessStateError(message, payload) {
		semantic = gatewayFailureSemanticAccountFault
	} else {
		semanticStatus := openAIStreamFailureStatus(payload, message)
		if semanticStatus == http.StatusForbidden {
			if openAIStream403AccountFailure(payload, message) {
				semantic = gatewayFailureSemanticAccountFault
			} else {
				semantic = gatewayFailureSemanticSharedFault
			}
		} else if semanticStatus != http.StatusTooManyRequests &&
			!isOpenAITransientProcessingError(http.StatusBadRequest, message, payload) {
			code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String()))
			if code == "" {
				code = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.code").String()))
			}
			errType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "response.error.type").String()))
			if errType == "" {
				errType = strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "error.type").String()))
			}
			combined := strings.ToLower(strings.TrimSpace(message + " " + code + " " + errType))
			for _, marker := range []string{
				"invalid_request",
				"content_policy",
				"policy",
				"safety",
				"high-risk cyber",
				"not allowed",
				"violat",
			} {
				if strings.Contains(combined, marker) {
					semantic = gatewayFailureSemanticSharedFault
					break
				}
			}
		}
	}
	return classifyGatewayFailover(gatewayFailoverObservation{
		Profile:    gatewayFailoverProfileOpenAI,
		Semantic:   semantic,
		StatusCode: openAIStreamFailureStatus(payload, message),
	}).RetryNextAccount
}

func openAIStreamErrorEventShouldFailover(payload []byte, message string) bool {
	semantic := gatewayFailureSemanticSharedFault
	if hit, _, _ := detectOpenAICyberPolicy(payload); hit {
		semantic = gatewayFailureSemanticSharedFault
	} else if isOpenAIContextWindowError(message, payload) {
		semantic = gatewayFailureSemanticSharedFault
	} else if isOpenAIUpstreamAccessStateError(message, payload) {
		semantic = gatewayFailureSemanticAccountFault
	} else {
		switch openAIStreamFailedEventSemanticStatus(payload, message) {
		case http.StatusForbidden:
			if openAIStream403AccountFailure(payload, message) {
				semantic = gatewayFailureSemanticAccountFault
			}
		case http.StatusUnauthorized, http.StatusTooManyRequests, 529:
			semantic = gatewayFailureSemanticAccountFault
		default:
			combined := strings.ToLower(strings.TrimSpace(message + " " +
				gjson.GetBytes(payload, "error.message").String() + " " +
				gjson.GetBytes(payload, "response.error.message").String()))
			if isOpenAITransientProcessingError(http.StatusBadRequest, message, payload) ||
				strings.Contains(combined, "temporary") ||
				strings.Contains(combined, "try again") ||
				strings.Contains(combined, "please retry") {
				semantic = gatewayFailureSemanticTransientFault
			}
		}
	}
	return classifyGatewayFailover(gatewayFailoverObservation{
		Profile:    gatewayFailoverProfileOpenAI,
		Semantic:   semantic,
		StatusCode: openAIStreamFailedEventSemanticStatus(payload, message),
	}).RetryNextAccount
}
