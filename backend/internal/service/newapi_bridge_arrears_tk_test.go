//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/require"
)

// arrearsBridgeError mirrors the real RelayErrorHandler output for a DashScope
// arrears 400: the upstream {"error":{"message":...,"type":"Arrearage",
// "code":"Arrearage"}} envelope is stored as a NewAPIError of errorType
// OpenAIError with RelayError = OpenAIError{...} (via types.WithOpenAIError).
func arrearsBridgeError(statusCode int, msg, typ, code string) *newapitypes.NewAPIError {
	return newapitypes.WithOpenAIError(newapitypes.OpenAIError{
		Message: msg,
		Type:    typ,
		Code:    code,
	}, statusCode)
}

const dashscopeArrearsMessage = "Access denied, please make sure your account is in good standing. For details, see: https://help.aliyun.com/zh/model-studio/error-code#overdue-payment"

const vstecscloudPrepaidQuotaMessage = "预扣费额度失败, 用户剩余额度: ¥0.031624, 需要预扣费额度: ¥0.470498 (request id: test-request)"

func newQwenArrearsAccount() *Account {
	// Mirrors prod account 60 "Qwen": fifth-platform newapi apikey account on
	// DashScope (channel_type=17), no pool mode, no custom error codes.
	return &Account{
		ID:          60,
		Name:        "Qwen",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 17,
	}
}

// Part A POSITIVE: the canonical DashScope Arrearage 400 (code/type == Arrearage)
// must be classified as arrears.
func TestTkIsBridgeUpstreamArrears_DashScopeArrearageCode(t *testing.T) {
	err := arrearsBridgeError(400, dashscopeArrearsMessage, "Arrearage", "Arrearage")
	require.True(t, tkIsBridgeUpstreamArrears(err))
}

// Part A POSITIVE: case-insensitive code/type, and the message-only variants
// ("account is in good standing" / "overdue" / "arrear") all match even when the
// provider does not surface a structured Arrearage code.
func TestTkIsBridgeUpstreamArrears_MessageAndCaseVariants(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *newapitypes.NewAPIError
	}{
		{"lowercase code", arrearsBridgeError(400, "x", "", "arrearage")},
		{"lowercase type", arrearsBridgeError(400, "x", "arrearage", "")},
		{"good standing phrase", arrearsBridgeError(400, dashscopeArrearsMessage, "invalid_request_error", "invalid")},
		{"overdue phrase", arrearsBridgeError(400, "Your payment is overdue, please recharge.", "billing_error", "billing")},
		{"arrear phrase", arrearsBridgeError(400, "Account in arrears.", "", "")},
		{"403 arrears", arrearsBridgeError(403, "x", "Arrearage", "Arrearage")},
		{"vstecscloud prepaid quota 403", arrearsBridgeError(403, vstecscloudPrepaidQuotaMessage, "insufficient_user_quota", "insufficient_user_quota")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, tkIsBridgeUpstreamArrears(tc.err))
		})
	}
}

func TestTkHandleBridgeArrearsPenalty_VstecscloudPrepaidQuotaUsesStandingBillingSSOT(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := &Account{
		ID:          101,
		Name:        "佳杰-stbl-5",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 1,
	}
	apiErr := arrearsBridgeError(
		http.StatusForbidden,
		vstecscloudPrepaidQuotaMessage,
		"insufficient_user_quota",
		"insufficient_user_quota",
	)

	handled := tkHandleBridgeArrearsPenalty(context.Background(), svc, account, apiErr)

	require.True(t, handled)
	require.Equal(t, 1, repo.setErrorCalls, "prepaid quota exhaustion must permanently stop scheduling")
	require.Zero(t, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "Account billing standing (403)")
	require.Contains(t, repo.lastErrorMsg, "预扣费额度失败")
	require.Equal(t, []string{tkStandingBillingIncidentReason}, blocker.reasons)
	require.Equal(t, []string{tkStandingBillingIncidentReason}, incidents.reasons)
	require.Len(t, incidents.details, 1)
	require.Contains(t, incidents.details[0], "用户剩余额度")
	require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(apiErr))
}

func TestTkHandleBridgeArrearsPenalty_StandingBillingMarkerOutsideMessageStillFailsOver(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := &Account{
		ID:          102,
		Name:        "newapi-billing-code-only",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 1,
	}
	apiErr := arrearsBridgeError(
		http.StatusForbidden,
		"billing rejected",
		"credit balance exhausted",
		"billing_error",
	)

	handled := tkHandleBridgeArrearsPenalty(context.Background(), svc, account, apiErr)

	require.True(t, handled)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, []string{tkStandingBillingIncidentReason}, blocker.reasons)
	require.Equal(t, []string{tkStandingBillingIncidentReason}, incidents.reasons)
	require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(apiErr),
		"penalty and failover must classify the same complete upstream envelope")
}

// Part A NEGATIVE: genuine client / validation / oversize 400s, model-not-found
// 404s, rate-limit 429s (including quota-type 429s without billing text) and 5xx
// outages must NEVER be classified as arrears — they carry no account-standing
// signal and penalizing them re-opens the #617 pool-drain or mis-disables RPM.
func TestTkIsBridgeUpstreamArrears_NonArrearsNeverMatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *newapitypes.NewAPIError
	}{
		{"client invalid_request", arrearsBridgeError(400, "Invalid value for parameter 'temperature'", "invalid_request_error", "invalid_value")},
		{"oversize body", arrearsBridgeError(400, "request body too large", "invalid_request_error", "request_too_large")},
		{"bad model name", arrearsBridgeError(400, "The supported API model names are ...", "invalid_request_error", "model_not_found")},
		{"model not found 404", arrearsBridgeError(404, "model_not_found", "not_found", "model_not_found")},
		{"rate limit 429", arrearsBridgeError(429, "Requests rate limit exceeded", "rate_limit_error", "rate_limit")},
		{"quota 429 without billing text", arrearsBridgeError(429, "Requests rate limit exceeded, please retry later", "exceeded_current_quota_error", "exceeded_current_quota_error")},
		{"insufficient_quota code in 429 message", arrearsBridgeError(429, "insufficient_quota", "insufficient_quota", "insufficient_quota")},
		{"server 500", arrearsBridgeError(500, "internal error", "server_error", "server_error")},
		{"nil", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, tkIsBridgeUpstreamArrears(tc.err))
		})
	}
}

const moonshotArrears429Message = "Your account org-ce9a5339d34042af92f21c9321c267f6 <ak-fbgy1mkyrfei11gu1to1> is suspended due to insufficient balance, please recharge your account or check your plan and billing details"

func newKimiArrearsAccount() *Account {
	// Mirrors prod account 83 "kimi": fifth-platform newapi apikey on
	// Moonshot China (channel_type=25). 2026-08-28 live probe: GET /v1/models
	// 200, chat 429 exceeded_current_quota_error + insufficient-balance suspend.
	return &Account{
		ID:          83,
		Name:        "kimi",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 25,
	}
}

// Part A POSITIVE (prod 2026-08-28 account 83): Moonshot bills standing-failure
// as HTTP 429 + exceeded_current_quota_error. That must classify as arrears so
// it is NOT treated as a 5s rate-limit cooldown.
func TestTkIsBridgeUpstreamArrears_MoonshotInsufficientBalance429(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *newapitypes.NewAPIError
	}{
		{"canonical moonshot 429", arrearsBridgeError(429, moonshotArrears429Message, "exceeded_current_quota_error", "")},
		{"suspended due to phrase", arrearsBridgeError(429, "your account is suspended due to overdue invoices", "rate_limit_error", "")},
		{"cn 余额不足 429", arrearsBridgeError(429, "账户余额不足，请充值后重试", "exceeded_current_quota_error", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, tkIsBridgeUpstreamArrears(tc.err))
		})
	}
}

// Part A: a Moonshot billing-standing 429 must DISABLE + alert via newapi_arrears,
// not temp-cool as a generic 429.
func TestTkHandleBridgeArrearsPenalty_Moonshot429DisablesAccountAndAlerts(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newKimiArrearsAccount()

	handled := tkHandleBridgeArrearsPenalty(context.Background(), svc,
		account, arrearsBridgeError(429, moonshotArrears429Message, "exceeded_current_quota_error", ""))

	require.True(t, handled, "moonshot billing 429 must be handled as arrears")
	require.Equal(t, 1, repo.setErrorCalls, "moonshot billing 429 must DISABLE via SetError")
	require.Zero(t, repo.tempCalls, "moonshot billing 429 must NOT temp-cool")
	require.Equal(t, []string{tkBridgeArrearsIncidentReason}, blocker.reasons)
	require.Equal(t, []string{tkBridgeArrearsIncidentReason}, incidents.reasons)
	require.Len(t, incidents.details, 1)
	require.Contains(t, incidents.details[0], "Account arrears (429)")
}

// Part A integration: the bridge penalty entrypoint must route a Moonshot
// billing-standing 429 through arrears BEFORE the generic 429 allowlist cooldown.
func TestTkHandleBridgeUpstreamPenalty_Moonshot429ArrearsNotRateLimit(t *testing.T) {
	require.True(t, tkBridgePenaltyStatusEligible(429),
		"precondition: 429 is on the generic penalty allowlist (rate-limit cooldown)")

	svc, repo, _, incidents := newBridgePenaltyTestService()
	account := newKimiArrearsAccount()

	tkHandleBridgeUpstreamPenalty(context.Background(), svc, account,
		arrearsBridgeError(429, moonshotArrears429Message, "exceeded_current_quota_error", ""))

	require.Equal(t, 1, repo.setErrorCalls, "moonshot billing 429 must disable via the arrears path")
	require.Zero(t, repo.tempCalls, "must not fall through to handle429 cooldown")
	require.Equal(t, []string{tkBridgeArrearsIncidentReason}, incidents.reasons)
}

// Part A: an arrears 400 must DISABLE the account (SetError, same as bridge 402)
// and route the incident with reason "newapi_arrears" to the notifier so it fails
// over / surfaces "no available accounts" instead of a pass-through 400.
func TestTkHandleBridgeArrearsPenalty_DisablesAccountAndAlerts(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()

	handled := tkHandleBridgeArrearsPenalty(context.Background(), svc,
		account, arrearsBridgeError(400, dashscopeArrearsMessage, "Arrearage", "Arrearage"))

	require.True(t, handled, "arrears must be handled (not passed through)")
	require.Equal(t, 1, repo.setErrorCalls, "arrears must DISABLE via SetError (same as 402)")
	require.Zero(t, repo.tempCalls, "arrears must NOT temp-cool (recharge requires manual recovery)")

	require.Equal(t, []string{tkBridgeArrearsIncidentReason}, blocker.reasons)
	require.Equal(t, []string{tkBridgeArrearsIncidentReason}, incidents.reasons)
	require.Len(t, incidents.details, 1)
	require.Contains(t, incidents.details[0], "Account arrears (400)",
		"Feishu detail must carry the upstream verdict")
}

// Part A NEGATIVE: a genuine client 400 must NOT be handled by the arrears path
// (returns false → no cooldown, no alert).
func TestTkHandleBridgeArrearsPenalty_ClientBadRequestNotHandled(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()

	handled := tkHandleBridgeArrearsPenalty(context.Background(), svc,
		account, arrearsBridgeError(400, "Invalid value for parameter 'temperature'", "invalid_request_error", "invalid_value"))

	require.False(t, handled)
	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, blocker.reasons)
	require.Empty(t, incidents.reasons)
}

// Part A integration: the bridge penalty entrypoint must route an arrears 400
// through the arrears path (disable + alert) even though 400 is excluded from the
// generic tkBridgePenaltyStatusEligible allowlist — i.e. the arrears exception
// runs BEFORE the allowlist skip.
func TestTkHandleBridgeUpstreamPenalty_ArrearsExceptionBeforeAllowlist(t *testing.T) {
	require.False(t, tkBridgePenaltyStatusEligible(400),
		"precondition: 400 is excluded from the generic penalty allowlist")

	svc, repo, _, incidents := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()

	tkHandleBridgeUpstreamPenalty(context.Background(), svc, account,
		arrearsBridgeError(400, dashscopeArrearsMessage, "Arrearage", "Arrearage"))

	require.Equal(t, 1, repo.setErrorCalls, "arrears 400 must disable via the exception path")
	require.Equal(t, []string{tkBridgeArrearsIncidentReason}, incidents.reasons)
}

// Part A: the bridge penalty entrypoint must still skip a generic client 400
// (no arrears markers) — the arrears exception must not widen the 400 allowlist.
func TestTkHandleBridgeUpstreamPenalty_GenericClient400StillSkipped(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()

	tkHandleBridgeUpstreamPenalty(context.Background(), svc, account,
		arrearsBridgeError(400, "Invalid value for parameter 'temperature'", "invalid_request_error", "invalid_value"))

	require.Zero(t, repo.tempCalls)
	require.Zero(t, repo.setErrorCalls)
	require.Empty(t, blocker.reasons)
	require.Empty(t, incidents.reasons)
}

// The penalty write must survive client cancellation (context.WithoutCancel).
func TestTkHandleBridgeArrearsPenalty_SurvivesCanceledContext(t *testing.T) {
	svc, repo, _, _ := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handled := tkHandleBridgeArrearsPenalty(ctx, svc, account,
		arrearsBridgeError(400, dashscopeArrearsMessage, "Arrearage", "Arrearage"))

	require.True(t, handled)
	require.Equal(t, 1, repo.setErrorCalls)
}

// nil safety: best-effort glue on the error path must never panic.
func TestTkHandleBridgeArrearsPenalty_NilSafety(t *testing.T) {
	svc, _, _, _ := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()
	require.False(t, tkHandleBridgeArrearsPenalty(context.Background(), nil, account, arrearsBridgeError(400, "x", "Arrearage", "Arrearage")))
	require.False(t, tkHandleBridgeArrearsPenalty(context.Background(), svc, nil, arrearsBridgeError(400, "x", "Arrearage", "Arrearage")))
	require.False(t, tkHandleBridgeArrearsPenalty(context.Background(), svc, account, nil))
}

// Part B: classifyIncident must route "newapi_arrears" to the IMMEDIATE P0 card
// (IncidentKindPermanentDisable), NOT the #730 default-OFF temporary digest, and
// carry the actionable recharge advice + the "上游账号欠费" label.
func TestClassifyIncident_NewAPIArrearsIsImmediateNotDigest(t *testing.T) {
	cls := classifyIncident(tkBridgeArrearsIncidentReason, time.Time{}, IncidentKindUnknown)
	require.True(t, cls.alert)
	require.Equal(t, IncidentKindPermanentDisable, cls.kind,
		"arrears must use the immediate P0 path, NOT the temporary digest")
	require.Equal(t, "上游账号欠费", cls.kindZh)
	require.Contains(t, cls.advice, "充值")
	require.Contains(t, cls.advice, "手动清除 error")
}

// Part B: the immediate (permanent) path is per-account deduped to once-per-hour
// window — a persistently-arrears account hitting every request must fire at
// most one card per window. Drive handlePermanent directly (the notifier-level
// dedupe), then assert a second hit inside the window is suppressed.
func TestHandlePermanent_NewAPIArrearsDedupedPerAccountWindow(t *testing.T) {
	notifier := newTKAccountIncidentNotifier(nil, "prod")
	account := newQwenArrearsAccount()
	cls := classifyIncident(tkBridgeArrearsIncidentReason, time.Time{}, IncidentKindUnknown)

	base := time.Now()
	notifier.now = func() time.Time { return base }
	notifier.handlePermanent(account, tkBridgeArrearsIncidentReason, cls, "Arrearage")
	_, seen := notifier.permSentAt[accountIncidentDedupeKey("prod", account.ID, cls.reasonClass)]
	require.True(t, seen, "first arrears alert must register the dedupe key")

	// A second hit one minute later (inside the 1h permanent-dedupe window) must
	// NOT update the timestamp (suppressed).
	firstAt := notifier.permSentAt[accountIncidentDedupeKey("prod", account.ID, cls.reasonClass)]
	notifier.now = func() time.Time { return base.Add(time.Minute) }
	notifier.handlePermanent(account, tkBridgeArrearsIncidentReason, cls, "Arrearage")
	require.Equal(t, firstAt, notifier.permSentAt[accountIncidentDedupeKey("prod", account.ID, cls.reasonClass)],
		"second arrears hit inside the dedupe window must be suppressed (one card per account per window)")
}

// The arrears detail line carries the upstream Arrearage code + message for the
// Feishu card.
func TestTkBridgeArrearsDetail_CarriesCodeAndMessage(t *testing.T) {
	detail := tkBridgeArrearsDetail(arrearsBridgeError(400, dashscopeArrearsMessage, "Arrearage", "Arrearage"))
	require.Contains(t, detail, "Arrearage")
	require.Contains(t, detail, "upstream code=Arrearage")
	require.Contains(t, detail, "https://help.aliyun.com/zh/model-studio/error-code#overdue-payment",
		"Feishu detail must keep the upstream help URL verbatim (MaskSensitiveInfo+lark_md would corrupt it)")
	require.NotContains(t, detail, "https://***.com")
	require.NotContains(t, detail, "https://.com")
	require.Empty(t, tkBridgeArrearsDetail(nil))
}
