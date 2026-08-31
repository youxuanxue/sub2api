package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// TK: account-standing billing / prepaid quota exhaustion — single matcher +
// single penalty + single Feishu reason.
//
// Prod 2026-08-30 account 93 tokensea-cc: upstream 403 "用户额度不足, 剩余额度: ¥-0.065926"
// was treated as a generic Anthropic permission 403. handle403 recorded stub
// saturation and failovers but did NOT SetError, so the prepaid-dead account
// stayed active+schedulable. Users saw mapped 502s; Feishu only fired the
// aggregate "真实用户体验受损" count card, never an account-failure P0.
//
// The standing-failure SSOT already exists for newapi arrears (DashScope 400
// Arrearage, Moonshot 429 insufficient-balance) and generic 402. This file
// lifts the billing-message matcher so every path — 400 credit balance, 402
// payment required, 403 prepaid 额度不足 — uses the same SetError + immediate
// P0 card (classifyIncident "newapi_arrears").
//
// Explicitly NOT standing (must fall through):
//   - weekly / 5h / 7d usage windows that reset (account 88 weekly quota)
//   - CloudWise per-model 402 (other models still work; handled earlier)
//   - CN-provider 402/429 (balance probe auto-recovers; leave that loop)
//   - RPM / rate-limit 429 without billing-standing text

// tkStandingBillingIncidentReason is the classifyIncident key for the immediate
// "上游账号欠费" P0 card. Reuse newapi_arrears so one Feishu class /
// 1h per-account dedupe covers DashScope, Moonshot, tokensea, and 402.
const tkStandingBillingIncidentReason = tkBridgeArrearsIncidentReason

// tkAccountStandingBilling429SafeMarkers is the 429-safe standing-failure set.
// Moonshot reuses exceeded_current_quota_error for both unpaid-account and RPM,
// so 429 must stay message-only and never include error-code names such as
// insufficient_quota. These four phrases are billing/account-suspend, not RPM.
var tkAccountStandingBilling429SafeMarkers = []string{
	"insufficient balance",
	"余额不足",
	"suspended due to",
	"please recharge your account",
}

// tkAccountStandingBillingMarkers is the 400/402/403 phrase list. It includes
// the 429-safe set plus prepaid-quota death text that arrives as 403/400
// (tokensea 用户额度不足, Anthropic credit balance). Do NOT add bare "quota"
// or insufficient_quota — those collide with RPM 429s if they leak into the
// 429 matcher.
var tkAccountStandingBillingMarkers = append([]string{
	"用户额度不足",
	"预扣费额度",
	"额度不足",
	"credit balance",
}, tkAccountStandingBilling429SafeMarkers...)

func tkIsAccountStandingBillingMessage(msg string) bool {
	if msg == "" {
		return false
	}
	for _, marker := range tkAccountStandingBillingMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func tkIsAccountStandingBillingFailure(upstreamMsg string, responseBody []byte) bool {
	haystack := strings.ToLower(strings.TrimSpace(upstreamMsg) + " " + string(responseBody))
	if haystack == "" {
		return false
	}
	if tkIsRecoverableUsageWindowMessage(haystack) {
		return false
	}
	return tkIsAccountStandingBillingMessage(haystack)
}

// tkIsRecoverableUsageWindowMessage is the negative SSOT: quota that resets
// without a human recharge is not account-standing death.
func tkIsRecoverableUsageWindowMessage(haystack string) bool {
	if haystack == "" {
		return false
	}
	for _, marker := range []string{
		"weekly usage quota",
		"exceeded the weekly",
		"5-hour",
		"7-day",
	} {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

func tkStandingBillingStatusEligible(statusCode int) bool {
	switch statusCode {
	case http.StatusBadRequest, http.StatusPaymentRequired, http.StatusForbidden:
		return true
	default:
		return false
	}
}

func tkStandingBillingErrorMsg(statusCode int, responseBody []byte) string {
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	if upstreamMsg != "" {
		upstreamMsg = truncateForLog([]byte(upstreamMsg), 512)
	}
	switch statusCode {
	case http.StatusPaymentRequired:
		if upstreamMsg == "" {
			return "Payment required (402): insufficient balance or billing issue"
		}
		return "Payment required (402): " + upstreamMsg
	case http.StatusBadRequest:
		if strings.Contains(strings.ToLower(upstreamMsg), "credit balance") {
			return "Credit balance exhausted (400): " + upstreamMsg
		}
		fallthrough
	default:
		if upstreamMsg == "" {
			return fmt.Sprintf("Account billing standing (%d): insufficient balance or prepaid quota exhausted", statusCode)
		}
		return fmt.Sprintf("Account billing standing (%d): %s", statusCode, upstreamMsg)
	}
}

// tkTryHandleStandingBilling disables the account and fires the immediate P0
// Feishu card when the upstream body is account-standing billing/quota death.
// Returns true when it handled the error (caller must not continue).
func (s *RateLimitService) tkTryHandleStandingBilling(ctx context.Context, account *Account, statusCode int, responseBody []byte) bool {
	if s == nil || account == nil || !tkStandingBillingStatusEligible(statusCode) {
		return false
	}
	if account.IsCNProvider() {
		return false
	}
	if !tkIsAccountStandingBillingFailure("", responseBody) {
		return false
	}
	errorMsg := tkStandingBillingErrorMsg(statusCode, responseBody)
	s.notifyAccountSchedulingBlocked(account, time.Time{}, tkStandingBillingIncidentReason, errorMsg)
	if s.accountRepo == nil {
		return true
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	if err := s.accountRepo.SetError(stateCtx, account.ID, errorMsg); err != nil {
		slog.Warn("account_standing_billing_set_error_failed",
			"account_id", account.ID, "status_code", statusCode, "error", err)
		return true
	}
	slog.Warn("account_disabled_standing_billing",
		"account_id", account.ID,
		"platform", account.Platform,
		"status_code", statusCode,
		"error", errorMsg,
	)
	return true
}
