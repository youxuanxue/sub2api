package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	newapitypes "github.com/QuantumNous/new-api/types"
)

// TK: account-standing Unpurchased entitlement — one matcher, one penalty,
// one failover semantic, one Feishu reason.
//
// Prod 2026-10-06 accounts 129/132 ali-token-plan (newapi channel_type=17):
// DashScope returned HTTP 403 AccessDenied.Unpurchased ("Access to model
// denied. Please make sure you are eligible for using the model."). The
// NewAPI bridge status matrix does not failover 403, and 403 is excluded
// from tkBridgePenaltyStatusEligible so HandleUpstreamError never ran.
// The dead credentials stayed active+schedulable while siblings served the
// same SKUs; users saw final 403.
//
// This is account-standing entitlement death, not billing and not a
// caller-owned model_not_found. Recharge / wait-for-window will not heal it.
// Ops must enable the model on the Aliyun Token Plan key (or replace the
// credential), then clear error and re-test.
//
// Explicitly NOT this class (must fall through):
//   - model_not_found / "does not exist or you do not have access" (#617)
//   - generic AccessDenied without Unpurchased
//   - HTML/WAF 403
//   - arrears / prepaid billing (owned by account_standing_billing_tk.go)

const (
	tkStandingUnpurchasedIncidentReason = "newapi_unpurchased"
	tkStandingUnpurchasedCode           = "accessdenied.unpurchased"
	tkStandingUnpurchasedEligiblePhrase = "eligible for using the model"
)

func tkIsAccountStandingUnpurchasedFailure(statusCode int, upstreamMsg string, responseBody []byte) bool {
	if statusCode != http.StatusForbidden {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(responseBody)))
	if code == tkStandingUnpurchasedCode {
		return true
	}
	haystack := strings.ToLower(strings.TrimSpace(upstreamMsg) + " " + string(responseBody))
	if haystack == "" {
		return false
	}
	return strings.Contains(haystack, tkStandingUnpurchasedCode) ||
		strings.Contains(haystack, tkStandingUnpurchasedEligiblePhrase)
}

func tkIsBridgeUpstreamUnpurchased(apiErr *newapitypes.NewAPIError) bool {
	if apiErr == nil {
		return false
	}
	return tkIsAccountStandingUnpurchasedFailure(
		apiErr.StatusCode,
		tkBridgeUpstreamRelayMessage(apiErr),
		tkBridgeUpstreamErrorBody(apiErr),
	)
}

func tkStandingUnpurchasedErrorMsg(statusCode int, responseBody []byte) string {
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	if upstreamMsg != "" {
		upstreamMsg = truncateForLog([]byte(upstreamMsg), 512)
	}
	code := strings.TrimSpace(extractUpstreamErrorCode(responseBody))
	head := fmt.Sprintf("Account model unpurchased (%d)", statusCode)
	if code != "" {
		head += ": " + code
	}
	if upstreamMsg == "" {
		return head
	}
	if code != "" && strings.Contains(strings.ToLower(upstreamMsg), strings.ToLower(code)) {
		return fmt.Sprintf("Account model unpurchased (%d): %s", statusCode, upstreamMsg)
	}
	return head + ": " + upstreamMsg
}

// tkTryHandleStandingUnpurchased disables the account (SetError also clears
// schedulable) and fires the immediate P0 Feishu card. Returns true when
// handled so callers must not continue the generic 403 ladder.
func (s *RateLimitService) tkTryHandleStandingUnpurchased(ctx context.Context, account *Account, statusCode int, responseBody []byte) bool {
	if s == nil || account == nil {
		return false
	}
	if !tkIsAccountStandingUnpurchasedFailure(statusCode, "", responseBody) {
		return false
	}
	errorMsg := tkStandingUnpurchasedErrorMsg(statusCode, responseBody)
	if s.tkTryHandleSupplierCredentialFailure(ctx, account, tkStandingUnpurchasedIncidentReason, errorMsg) {
		return true
	}
	s.notifyAccountSchedulingBlocked(account, time.Time{}, tkStandingUnpurchasedIncidentReason, errorMsg)
	if s.accountRepo == nil {
		return true
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	if err := s.accountRepo.SetError(stateCtx, account.ID, errorMsg); err != nil {
		slog.Warn("account_standing_unpurchased_set_error_failed",
			"account_id", account.ID, "status_code", statusCode, "error", err)
		return true
	}
	slog.Warn("account_disabled_standing_unpurchased",
		"account_id", account.ID,
		"platform", account.Platform,
		"channel_type", account.ChannelType,
		"status_code", statusCode,
		"error", errorMsg,
	)
	return true
}
