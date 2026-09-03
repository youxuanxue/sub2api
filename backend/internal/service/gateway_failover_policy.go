package service

import "net/http"

// gatewayFailoverProfile identifies a transport contract, not a provider-owned
// policy. Every profile is evaluated by classifyGatewayFailover so platform and
// channel adapters cannot grow independent retry-next-account rules.
type gatewayFailoverProfile uint8

const (
	gatewayFailoverProfileUnknown gatewayFailoverProfile = iota
	gatewayFailoverProfileGeneric
	gatewayFailoverProfileOpenAI
	gatewayFailoverProfileGoogle
	gatewayFailoverProfileGrok
	gatewayFailoverProfileNewAPIBridge
	gatewayFailoverProfileOpenAIPassthrough
)

// gatewayFailureSemantic is produced by protocol-specific body parsers. The
// policy owns what that semantic means for account failover.
type gatewayFailureSemantic uint8

const (
	gatewayFailureSemanticUnclassified gatewayFailureSemantic = iota
	gatewayFailureSemanticTerminalRequest
	gatewayFailureSemanticRetryableAccount
	gatewayFailureSemanticRetryableTransient
)

type gatewayFailoverObservation struct {
	Profile                gatewayFailoverProfile
	Semantic               gatewayFailureSemantic
	StatusCode             int
	AccountType            string
	AccountConfiguredRetry bool
}

type gatewayFailoverDecision struct {
	RetryNextAccount bool
}

// classifyGatewayFailover is the only owner of the retry-next-account
// decision. Protocol adapters may classify provider payloads into Semantic,
// but must not independently turn statuses or semantics into a boolean.
func classifyGatewayFailover(obs gatewayFailoverObservation) gatewayFailoverDecision {
	switch obs.Profile {
	case gatewayFailoverProfileGeneric, gatewayFailoverProfileOpenAI,
		gatewayFailoverProfileGoogle, gatewayFailoverProfileGrok,
		gatewayFailoverProfileNewAPIBridge, gatewayFailoverProfileOpenAIPassthrough:
		// Known profile; continue into semantic and status policy.
	default:
		return gatewayFailoverDecision{}
	}

	switch obs.Semantic {
	case gatewayFailureSemanticTerminalRequest:
		return gatewayFailoverDecision{}
	case gatewayFailureSemanticRetryableAccount, gatewayFailureSemanticRetryableTransient:
		return gatewayFailoverDecision{RetryNextAccount: true}
	case gatewayFailureSemanticUnclassified:
		// Continue into the centrally owned transport matrix.
	default:
		return gatewayFailoverDecision{}
	}

	switch obs.Profile {
	case gatewayFailoverProfileGeneric:
		return gatewayFailoverDecision{RetryNextAccount: genericGatewayFailoverStatus(obs.StatusCode)}
	case gatewayFailoverProfileOpenAI, gatewayFailoverProfileGrok:
		return gatewayFailoverDecision{RetryNextAccount: openAIGatewayFailoverStatus(obs.StatusCode)}
	case gatewayFailoverProfileGoogle:
		return gatewayFailoverDecision{RetryNextAccount: googleGatewayFailoverStatus(obs.StatusCode)}
	case gatewayFailoverProfileNewAPIBridge:
		return gatewayFailoverDecision{RetryNextAccount: newAPIBridgeFailoverStatus(obs.StatusCode)}
	case gatewayFailoverProfileOpenAIPassthrough:
		return gatewayFailoverDecision{RetryNextAccount: openAIPassthroughFailoverStatus(obs)}
	default:
		return gatewayFailoverDecision{}
	}
}

func genericGatewayFailoverStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusFailedDependency, http.StatusTooManyRequests, 529:
		return true
	default:
		return statusCode >= 500
	}
}

func openAIGatewayFailoverStatus(statusCode int) bool {
	return statusCode == http.StatusMethodNotAllowed || genericGatewayFailoverStatus(statusCode)
}

func googleGatewayFailoverStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, 529:
		return true
	default:
		return statusCode >= 500
	}
}

func newAPIBridgeFailoverStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func openAIPassthroughFailoverStatus(obs gatewayFailoverObservation) bool {
	if obs.AccountConfiguredRetry {
		return true
	}
	if obs.StatusCode == http.StatusTooManyRequests || obs.StatusCode == 529 {
		return true
	}
	if obs.AccountType != AccountTypeAPIKey {
		return false
	}
	switch obs.StatusCode {
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		520, 521, 522, 523, 524:
		return true
	default:
		return false
	}
}
