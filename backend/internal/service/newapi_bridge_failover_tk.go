package service

import (
	"context"
	"net/http"

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
	semantic := gatewayFailureSemanticUnclassified
	if tkIsBridgeUpstreamArrears(apiErr) {
		semantic = gatewayFailureSemanticRetryableAccount
	}
	return classifyGatewayFailover(gatewayFailoverObservation{
		Profile:    gatewayFailoverProfileNewAPIBridge,
		Semantic:   semantic,
		StatusCode: apiErr.StatusCode,
	}).RetryNextAccount
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
	return &UpstreamFailoverError{
		StatusCode:        statusCode,
		ResponseBody:      body,
		NextAccountAction: NextAccountRetry,
	}
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
