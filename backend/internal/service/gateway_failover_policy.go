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

// gatewayFailureSemantic is factual evidence produced by protocol-specific
// body parsers. Its names deliberately avoid retry/stop language: only the
// global policy may turn these facts into an account-failover decision.
type gatewayFailureSemantic uint8

const (
	gatewayFailureSemanticUnclassified gatewayFailureSemantic = iota
	gatewayFailureSemanticSharedFault
	gatewayFailureSemanticAccountFault
	gatewayFailureSemanticTransientFault
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
	case gatewayFailureSemanticSharedFault:
		return gatewayFailoverDecision{}
	case gatewayFailureSemanticAccountFault, gatewayFailureSemanticTransientFault:
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

// classifyGatewayFailoverError is the runtime choke point used by handlers.
// New code records normalized evidence on the error via applyGatewayFailoverSemantic;
// legacy composite literals are adapted here so their historical zero-value
// retry behavior remains intact while the final boolean still has one owner.
func classifyGatewayFailoverError(failoverErr *UpstreamFailoverError) gatewayFailoverDecision {
	if failoverErr == nil {
		return gatewayFailoverDecision{}
	}
	if failoverErr.failoverObservation != nil {
		return classifyGatewayFailover(*failoverErr.failoverObservation)
	}
	return classifyGatewayFailover(gatewayFailoverObservation{
		Profile:  gatewayFailoverProfileGeneric,
		Semantic: gatewayFailureSemanticFromLegacyAction(failoverErr.NextAccountAction),
	})
}

func gatewayFailureSemanticFromLegacyAction(action NextAccountAction) gatewayFailureSemantic {
	switch action {
	case NextAccountLegacyRetry, NextAccountRetry:
		return gatewayFailureSemanticAccountFault
	case NextAccountStop:
		return gatewayFailureSemanticSharedFault
	default:
		return gatewayFailureSemantic(255)
	}
}

// applyGatewayFailoverSemantic stores adapter evidence and materializes the
// legacy action field for compatibility with existing diagnostics and tests.
// The action is derived from the global policy; adapters never set it directly.
func applyGatewayFailoverSemantic(
	failoverErr *UpstreamFailoverError,
	profile gatewayFailoverProfile,
	semantic gatewayFailureSemantic,
) *UpstreamFailoverError {
	if failoverErr == nil {
		return nil
	}
	observation := gatewayFailoverObservation{Profile: profile, Semantic: semantic}
	failoverErr.failoverObservation = &observation
	if classifyGatewayFailover(observation).RetryNextAccount {
		failoverErr.NextAccountAction = NextAccountRetry
	} else {
		failoverErr.NextAccountAction = NextAccountStop
	}
	return failoverErr
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
