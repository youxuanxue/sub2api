package service

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// tkBridgeUpstreamShouldFailoverAfterPenalty reports whether a bridge upstream
// error should trigger the handler's existing failedAccountIDs failover loop
// after any account-level penalty has been applied. Account-standing failures
// qualify, as do gateway outage statuses that are safe to retry on another
// provider without mutating account state. Client-induced 400/404 and all other
// 5xx remain terminal to avoid draining the pool (#617 class).
func tkBridgeUpstreamShouldFailoverAfterPenalty(apiErr *newapitypes.NewAPIError) bool {
	if apiErr == nil {
		return false
	}
	return classifyGatewayFailover(gatewayFailoverObservation{
		Profile:    gatewayFailoverProfileNewAPIBridge,
		Semantic:   tkBridgeFailureSemantic(apiErr),
		StatusCode: apiErr.StatusCode,
	}).RetryNextAccount
}

func tkBridgeFailureSemantic(apiErr *newapitypes.NewAPIError) gatewayFailureSemantic {
	if tkIsBridgeUpstreamArrears(apiErr) {
		return gatewayFailureSemanticAccountFault
	}
	if apiErr != nil {
		if upstream, ok := tkBridgeUpstreamOpenAIError(apiErr); ok && tkSupplierThinkingToolPreflight(apiErr.StatusCode, upstream.Message) {
			// A supplier preflight rejected this model/feature combination before
			// execution. Another endpoint may satisfy the original request.
			return gatewayFailureSemanticTransientFault
		}
	}
	if apiErr != nil && apiErr.StatusCode == http.StatusInternalServerError {
		if upstream, ok := tkBridgeUpstreamOpenAIError(apiErr); ok {
			message := strings.ToLower(strings.TrimSpace(tkBridgeDecodeSupplierMessage(upstream.Message)))
			message, _, _ = strings.Cut(message, " (request id:")
			// These envelopes describe the supplier's own unavailable upstream,
			// not invalid client input or evidence that our credential is revoked.
			switch message {
			case "upstream access forbidden, please contact administrator", "没有可用账号，请稍后重试":
				return gatewayFailureSemanticTransientFault
			}
		}
	}
	return gatewayFailureSemanticUnclassified
}

func tkSupplierThinkingToolPreflight(status int, message string) bool {
	return status == http.StatusBadRequest && strings.HasPrefix(strings.TrimSpace(message), "[preflight:R3.forced_tool_choice_incompatible]")
}

// Some relays encode UTF-8 error bytes as Latin-1 characters in their JSON.
func tkBridgeDecodeSupplierMessage(message string) string {
	decoded := make([]byte, 0, len(message))
	for _, r := range message {
		if r > 255 {
			return message
		}
		decoded = append(decoded, byte(r))
	}
	if utf8.Valid(decoded) {
		return string(decoded)
	}
	return message
}

func tkNewAPIBridgeUpstreamFailoverError(c *gin.Context, apiErr *newapitypes.NewAPIError) *UpstreamFailoverError {
	statusCode := http.StatusBadGateway
	var body []byte
	if apiErr != nil {
		statusCode = apiErr.StatusCode
		body = tkBridgeUpstreamErrorBody(apiErr)
		if c != nil {
			TkRecordBridgeUpstreamError(c, statusCode, apiErr)
		}
	}
	semantic := tkBridgeFailureSemantic(apiErr)
	if semantic == gatewayFailureSemanticUnclassified {
		semantic = gatewayFailureSemanticAccountFault
	}
	return applyGatewayFailoverSemantic(&UpstreamFailoverError{
		StatusCode:             statusCode,
		ResponseBody:           body,
		RequestScopedTransient: semantic == gatewayFailureSemanticTransientFault,
	}, gatewayFailoverProfileNewAPIBridge, semantic)
}

func bridgeWrapRelayErrorAfterPenalty(
	ctx context.Context,
	rls *RateLimitService,
	c *gin.Context,
	account *Account,
	apiErr *newapitypes.NewAPIError,
) error {
	tkHandleBridgeUpstreamPenalty(ctx, rls, account, apiErr)
	if tkBridgeUpstreamShouldFailoverAfterPenalty(apiErr) {
		return tkNewAPIBridgeUpstreamFailoverError(c, apiErr)
	}
	return tkWrapBridgeRelayError(c, apiErr)
}
