package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *GatewayHandler) handleKiroContentFilteredError(c *gin.Context, err error) bool {
	var contentFilteredErr *service.KiroContentFilteredError
	if !errors.As(err, &contentFilteredErr) {
		return false
	}
	service.MarkOpsClientContentFiltered(c)
	c.Header(service.KiroOutcomeHeader, service.KiroContentFilteredOutcome)
	h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", service.KiroContentFilteredClientMessage())
	return true
}

// tkHandleMessagesNonFailoverForwardError handles Messages-path forward errors that
// must return immediately without failover (Kiro content-filter / invalid model /
// invalid request / quota + TK canonical ingress UA reject).
func (h *GatewayHandler) tkHandleMessagesNonFailoverForwardError(c *gin.Context, err error) bool {
	if h.handleKiroContentFilteredError(c, err) {
		return true
	}

	// Kiro upstream rejected the model (HTTP 400 INVALID_MODEL_ID):
	// return 400 immediately with a clear message, no failover (every
	// Kiro account rejects the same unknown model identically).
	var kiroInvalidModelErr *service.KiroInvalidModelError
	if errors.As(err, &kiroInvalidModelErr) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", kiroInvalidModelErr.ClientMessage())
		return true
	}

	var kiroInvalidRequestErr *service.KiroInvalidRequestError
	if errors.As(err, &kiroInvalidRequestErr) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", kiroInvalidRequestErr.ClientMessage())
		return true
	}

	var kiroQuotaErr *service.KiroEndpointQuotaExhaustedError
	if errors.As(err, &kiroQuotaErr) {
		c.Header("Retry-After", strconv.Itoa(service.KiroEndpointQuotaExhaustedRetryAfterSeconds()))
		h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", kiroQuotaErr.ClientMessage())
		return true
	}

	// TK canonical-OAuth ingress UA reject: 403 immediately, no failover.
	// Local policy denial, not account/provider health — mark it
	// business-limited so strict-mode canary reject volume stays out of
	// error-rate dashboards (mirrors the BetaBlockedError branch above).
	// See gateway_service_tk_canonical_oauth_guard.go.
	var canonicalUARejectErr *service.CanonicalIngressUARejectedError
	if errors.As(err, &canonicalUARejectErr) {
		service.MarkOpsClientPolicyDenied(c, service.OpsClientPolicyDeniedReasonLocalPolicyDenied)
		h.errorResponse(c, http.StatusForbidden, "permission_error", canonicalUARejectErr.Error())
		return true
	}

	return false
}

func (h *GatewayHandler) tkConsumeKiroSessionRecovery(ctx context.Context, reqLog *zap.Logger, platform string, groupID *int64, sessionKey string) int64 {
	if platform != service.PlatformKiro || sessionKey == "" {
		return 0
	}
	recoveryExcludedAccountID, recoveryErr := h.gatewayService.ConsumeKiroSessionRecovery(ctx, groupID, sessionKey)
	if recoveryErr != nil {
		reqLog.Warn("gateway.kiro_session_recovery_consume_failed", zap.Error(recoveryErr))
		return 0
	}
	if recoveryExcludedAccountID > 0 {
		reqLog.Warn("gateway.kiro_session_recovery_excluding_account",
			zap.Int64("account_id", recoveryExcludedAccountID),
		)
	}
	return recoveryExcludedAccountID
}

func tkSeedFailoverExcludedAccount(fs *FailoverState, excludedID *int64) {
	if fs == nil || excludedID == nil || *excludedID <= 0 {
		return
	}
	fs.FailedAccountIDs[*excludedID] = struct{}{}
	*excludedID = 0
}

func (h *GatewayHandler) tkRememberKiroSessionRecoveryOnDisconnect(c *gin.Context, reqLog *zap.Logger, groupID *int64, sessionKey string, accountID int64, err error) {
	if !service.IsKiroPostOutputStreamDisconnect(err) || sessionKey == "" {
		return
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 2*time.Second)
	recoveryErr := h.gatewayService.RememberKiroSessionRecovery(recoveryCtx, groupID, sessionKey, accountID)
	cancelRecovery()
	if recoveryErr != nil {
		reqLog.Warn("gateway.kiro_session_recovery_record_failed",
			zap.Int64("account_id", accountID),
			zap.Error(recoveryErr),
		)
	} else {
		reqLog.Warn("gateway.kiro_session_recovery_recorded",
			zap.Int64("account_id", accountID),
		)
	}
}
