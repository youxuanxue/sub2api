package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/tidwall/gjson"
)

// TK: newapi fifth-platform bridge → upstream account-standing / arrears (欠费)
// classifier + account-level penalty + immediate Feishu alert.
//
// Why this file exists:
//
//  1. prod 2026-06-12, account 60 "Qwen" / Alibaba DashScope (channel_type=17):
//     arrears arrives as HTTP 400 + {"error":{"type":"Arrearage","code":"Arrearage",
//     "message":"Access denied, please make sure your account is in good standing."}}.
//  2. prod 2026-08-28, account 83 "kimi" / Moonshot China (channel_type=25):
//     arrears arrives as HTTP 429 + type=exceeded_current_quota_error + message
//     "is suspended due to insufficient balance, please recharge your account".
//     GET /v1/models still 200 — the key is valid, billing standing is not.
//
// TokenKey classifies a bridge 400 as CLIENT-induced (see
// tkBridgePenaltyStatusEligible — 400 is excluded from the penalty allowlist
// precisely so a malformed caller request can never disable a shared account, the
// #617 lesson). That is correct for validation / bad-param / oversized-body
// 400s, but an arrears 400 is an ACCOUNT-standing failure that is persistent
// until a human recharges the upstream console. Left as a pass-through 400 the
// dead account stays active + schedulable, every request fails identically with
// a confusing "400 bad request", AND operators get NO alert that the Alibaba
// account needs a recharge.
//
// A Moonshot billing 429 is worse if left on the generic 429 path: HandleUpstreamError
// applyCNProviderReactive429 does not apply (platform=newapi is not IsCNProvider),
// so the account gets a ~5s cooldown and keeps rotating. User-visible status is
// "Upstream rate limit exceeded" while the real verdict is unpaid / suspended.
//
// This file closes that gap WITHOUT widening the 400 allowlist (which would
// re-open the #617 pool-drain) and WITHOUT treating every 429 quota type as
// arrears (exceeded_current_quota_error is also used for RPM). It is a separate,
// deliberately narrow exception:
//
//   - Part A (penalty): the arrears signal is matched conservatively.
//     400/403: provider error code/type == "Arrearage" case-insensitive, OR
//     message containing "account is in good standing" / "overdue" / "arrear",
//     OR the billing-standing phrases below.
//     429: ONLY billing-standing message phrases (insufficient balance /
//     余额不足 / suspended due to / please recharge your account). Never match
//     type/code alone — Moonshot RPM 429s reuse exceeded_current_quota_error.
//     On a match the account is DISABLED (SetError, same as bridge 402 balance)
//     so recharge alone does not auto-recover — ops must clear error + re-test
//     in Admin.
//   - Part B (alert): the same path routes the incident through the IMMEDIATE
//     P0 Feishu card (classifyIncident "newapi_arrears" → IncidentKindPermanent
//     Disable), NOT the self-healing temporary-cooldown digest that #730 made
//     default-OFF. Arrears does not self-heal — it needs a human — so it must be
//     a visible, actionable card. Per-account dedupe (the 1h permanent-dedupe
//     window inside handlePermanent) keeps a persistently-arrears account from
//     spamming one card per request.
//
// Narrowness is the whole point: a false positive disables + alerts on a healthy
// account, so the matcher must NEVER fire on a generic client 400 or a plain
// rate-limit 429.

// tkBridgeArrearsIncidentReason is the stable reason string classifyIncident
// maps to the immediate "上游账号欠费" P0 card (NOT the temporary digest).
const tkBridgeArrearsIncidentReason = "newapi_arrears"

// tkIsBridgeUpstreamArrears reports whether a bridge upstream error is an
// account-standing / arrears (欠费) signal that warrants an account-level
// penalty + immediate alert. It inspects the synthesized OpenAI-style envelope
// (tkBridgeUpstreamErrorBody) because RelayErrorHandler stores the DashScope
// error.message / error.type / error.code inside NewAPIError.RelayError, which
// only ToOpenAIError() faithfully projects (NewAPIError.Error() returns the bare
// error code when the underlying Err is nil).
//
// Match ONLY the account-standing signal. Generic invalid_request_error /
// bad-param / oversize 400s and RPM 429s carry none of these and must fall
// through unchanged.
func tkIsBridgeUpstreamArrears(apiErr *newapitypes.NewAPIError) bool {
	if apiErr == nil {
		return false
	}
	// Conservative status gate: DashScope/百炼 arrears is 400 (occasionally 403);
	// Moonshot China billing standing-failure is 429 + billing text. Never let a
	// 5xx outage or a 429 without billing-standing phrases reach SetError.
	switch apiErr.StatusCode {
	case 400, 403, 429:
	default:
		return false
	}
	body := tkBridgeUpstreamErrorBody(apiErr)
	if len(body) == 0 {
		return false
	}
	msg := strings.ToLower(gjson.GetBytes(body, "error.message").String())
	// 429: message-only. Do not trust error.type/code — Moonshot reuses
	// exceeded_current_quota_error for both unpaid-account and RPM.
	if apiErr.StatusCode == 429 {
		return tkIsBridgeUpstreamArrearsBillingMessage(msg)
	}
	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	typ := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.type").String()))
	if code == "arrearage" || typ == "arrearage" {
		return true
	}
	if msg == "" {
		return false
	}
	for _, marker := range []string{
		"account is in good standing", // DashScope arrears verbatim phrase
		"overdue",                     // overdue-payment
		"arrear",                      // arrears / arrearage (substring)
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return tkIsBridgeUpstreamArrearsBillingMessage(msg)
}

// tkIsBridgeUpstreamArrearsBillingMessage is the 429-safe standing-failure set.
// These phrases are billing/account-suspend, not RPM. "exceeded_current_quota_error"
// alone is intentionally NOT a marker.
func tkIsBridgeUpstreamArrearsBillingMessage(msg string) bool {
	if msg == "" {
		return false
	}
	for _, marker := range []string{
		"insufficient balance",
		"余额不足",
		"suspended due to",
		"please recharge your account",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// tkBridgeArrearsDetail synthesizes the Feishu card 详情 line: the upstream code
// ("Arrearage") plus the truncated upstream message, so operators see WHICH
// upstream account-standing verdict fired without querying the DB.
func tkBridgeArrearsDetail(apiErr *newapitypes.NewAPIError) string {
	if apiErr == nil {
		return ""
	}
	oai, ok := tkBridgeUpstreamOpenAIError(apiErr)
	if !ok {
		return ""
	}
	code := strings.TrimSpace(fmt.Sprint(oai.Code))
	if code == "" || code == "<nil>" {
		code = strings.TrimSpace(oai.Type)
	}
	msg := strings.TrimSpace(oai.Message)
	if code == "" && msg == "" {
		return ""
	}
	head := "Arrearage"
	if code != "" {
		head = "Arrearage (upstream code=" + code + ")"
	}
	if msg == "" {
		return head
	}
	return head + ": " + truncateForLog([]byte(msg), 256)
}

// tkHandleBridgeArrearsPenalty applies the account-level penalty for an upstream
// arrears 400 and emits the immediate P0 Feishu alert. Returns true when it
// handled the error (caller must NOT then run the generic status-allowlist
// penalty). The account-state write survives client cancellation via
// openAIAccountStateContext (context.WithoutCancel), like the sibling penalty.
func tkHandleBridgeArrearsPenalty(ctx context.Context, rls *RateLimitService, account *Account, apiErr *newapitypes.NewAPIError) bool {
	if rls == nil || account == nil || apiErr == nil {
		return false
	}
	if !tkIsBridgeUpstreamArrears(apiErr) {
		return false
	}
	detail := tkBridgeArrearsDetail(apiErr)
	errorMsg := fmt.Sprintf("Account arrears (%d): insufficient balance or billing issue", apiErr.StatusCode)
	if detail != "" {
		errorMsg = fmt.Sprintf("Account arrears (%d): %s", apiErr.StatusCode, detail)
	}

	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()

	// Same penalty shape as bridge 402: permanent disable until ops clears error.
	rls.notifyAccountSchedulingBlocked(account, time.Time{}, tkBridgeArrearsIncidentReason, errorMsg)
	if err := rls.accountRepo.SetError(stateCtx, account.ID, errorMsg); err != nil {
		slog.Warn("newapi_bridge_arrears_set_error_failed",
			"account_id", account.ID, "error", err)
		return true
	}
	slog.Warn("account_disabled_newapi_arrears",
		"account_id", account.ID,
		"platform", account.Platform,
		"channel_type", account.ChannelType,
		"status_code", apiErr.StatusCode,
		"error", errorMsg,
	)
	return true
}
