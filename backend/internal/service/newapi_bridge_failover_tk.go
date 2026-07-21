package service

import (
	"context"
	"net/http"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
)

// tkBridgeUpstreamShouldFailoverAfterPenalty reports whether a bridge upstream
// error should trigger the handler's existing failedAccountIDs failover loop
// after the account-level penalty has been applied. Only account-standing /
// credential / capacity failures qualify — never client-induced 400/404 or 5xx
// provider outages (#617 pool-drain class).
func tkBridgeUpstreamShouldFailoverAfterPenalty(apiErr *newapitypes.NewAPIError) bool {
	if apiErr == nil {
		return false
	}
	if tkIsBridgeUpstreamArrears(apiErr) {
		return true
	}
	return tkBridgePenaltyStatusEligible(apiErr.StatusCode)
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
