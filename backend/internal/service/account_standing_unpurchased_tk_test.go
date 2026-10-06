//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const dashscopeUnpurchasedMessage = "Access to model denied. Please make sure you are eligible for using the model."

const dashscopeUnpurchasedBody = `{"error":{"code":"AccessDenied.Unpurchased","message":"Access to model denied. Please make sure you are eligible for using the model.","type":"AccessDenied.Unpurchased"}}`

func TestTkIsAccountStandingUnpurchasedFailure_PositiveAndNegative(t *testing.T) {
	t.Parallel()
	require.True(t, tkIsAccountStandingUnpurchasedFailure(
		http.StatusForbidden, "", []byte(dashscopeUnpurchasedBody)))
	require.True(t, tkIsAccountStandingUnpurchasedFailure(
		http.StatusForbidden, dashscopeUnpurchasedMessage, nil))
	require.True(t, tkIsAccountStandingUnpurchasedFailure(
		http.StatusForbidden,
		"AccessDenied.Unpurchased: Access to model denied.",
		[]byte(`{"error":{"message":"Access to model denied."}}`),
	))

	require.False(t, tkIsAccountStandingUnpurchasedFailure(
		http.StatusBadRequest, dashscopeUnpurchasedMessage, []byte(dashscopeUnpurchasedBody)),
		"Unpurchased is a 403 standing failure, not a client 400")
	require.False(t, tkIsAccountStandingUnpurchasedFailure(
		http.StatusForbidden, "", []byte(`{"error":{"code":"model_not_found","message":"The model does not exist or you do not have access to it."}}`)))
	require.False(t, tkIsAccountStandingUnpurchasedFailure(
		http.StatusForbidden, "Invalid API key", nil))
	require.False(t, tkIsAccountStandingUnpurchasedFailure(
		http.StatusForbidden, "Access denied, please make sure your account is in good standing.", nil),
		"DashScope arrears stays on the billing SSOT")
	require.False(t, tkIsAccountStandingUnpurchasedFailure(http.StatusForbidden, "", nil))
}

func TestTkIsBridgeUpstreamUnpurchased_UsesCompleteEnvelope(t *testing.T) {
	t.Parallel()
	err := arrearsBridgeError(http.StatusForbidden, dashscopeUnpurchasedMessage, "AccessDenied.Unpurchased", "AccessDenied.Unpurchased")
	require.True(t, tkIsBridgeUpstreamUnpurchased(err))
	require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(newQwenArrearsAccount(), err),
		"penalty and failover must share the Unpurchased SSOT")
	require.False(t, tkIsBridgeUpstreamUnpurchased(arrearsBridgeError(http.StatusForbidden, "Access forbidden", "access_denied", "AccessDenied")))
	require.False(t, tkIsBridgeUpstreamUnpurchased(nil))
}

func TestTkTryHandleStandingUnpurchased_DisablesAndAlerts(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()
	account.ID = 129
	account.Name = "ali-token-plan"

	handled := svc.tkTryHandleStandingUnpurchased(
		context.Background(),
		account,
		http.StatusForbidden,
		[]byte(dashscopeUnpurchasedBody),
	)

	require.True(t, handled)
	require.Equal(t, 1, repo.setErrorCalls, "Unpurchased 403 must SetError (schedulable=false)")
	require.Zero(t, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "AccessDenied.Unpurchased")
	require.Equal(t, []string{tkStandingUnpurchasedIncidentReason}, blocker.reasons)
	require.Equal(t, []string{tkStandingUnpurchasedIncidentReason}, incidents.reasons)
	require.Contains(t, incidents.details[0], "eligible for using the model")
}

func TestTkHandleBridgeUpstreamPenalty_UnpurchasedBeforeAllowlist(t *testing.T) {
	require.False(t, tkBridgePenaltyStatusEligible(http.StatusForbidden),
		"precondition: generic 403 must stay off the bridge penalty allowlist")

	svc, repo, _, incidents := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()
	account.ID = 129

	tkHandleBridgeUpstreamPenalty(context.Background(), svc, account,
		arrearsBridgeError(http.StatusForbidden, dashscopeUnpurchasedMessage, "AccessDenied.Unpurchased", "AccessDenied.Unpurchased"))

	require.Equal(t, 1, repo.setErrorCalls)
	require.Zero(t, repo.tempCalls)
	require.Equal(t, []string{tkStandingUnpurchasedIncidentReason}, incidents.reasons)
}

func TestTkHandleBridgeUpstreamPenalty_Generic403StillSkipped(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()

	tkHandleBridgeUpstreamPenalty(context.Background(), svc, account,
		arrearsBridgeError(http.StatusForbidden, "Access forbidden by WAF", "access_denied", "AccessDenied"))

	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, blocker.reasons)
	require.Empty(t, incidents.reasons)
	require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(account,
		arrearsBridgeError(http.StatusForbidden, "Access forbidden by WAF", "access_denied", "AccessDenied")))
}

func TestTkTryHandleStandingUnpurchased_SurvivesCanceledContext(t *testing.T) {
	svc, repo, _, _ := newBridgePenaltyTestService()
	repo.failSetErrorOnCanceled = true
	account := newQwenArrearsAccount()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handled := svc.tkTryHandleStandingUnpurchased(ctx, account, http.StatusForbidden, []byte(dashscopeUnpurchasedBody))

	require.True(t, handled)
	require.Equal(t, 1, repo.setErrorCalls)
}

func TestHandleUpstreamError_Unpurchased403DisablesNewAPIAccount(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newQwenArrearsAccount()
	account.ID = 132
	account.Name = "ali-token-plan-2"

	shouldDisable := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(dashscopeUnpurchasedBody),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Zero(t, repo.tempCalls)
	require.Equal(t, []string{tkStandingUnpurchasedIncidentReason}, blocker.reasons)
	require.Equal(t, []string{tkStandingUnpurchasedIncidentReason}, incidents.reasons)
}

func TestClassifyIncident_UnpurchasedIsImmediateNotArrears(t *testing.T) {
	cls := classifyIncident(tkStandingUnpurchasedIncidentReason, time.Time{}, IncidentKindUnknown)
	require.True(t, cls.alert)
	require.Equal(t, IncidentKindPermanentDisable, cls.kind)
	require.Equal(t, "newapi_unpurchased", cls.reasonClass)
	require.Equal(t, "上游模型未开通", cls.kindZh)
	require.Contains(t, cls.advice, "开通")
	require.NotEqual(t, "上游账号欠费", cls.kindZh, "Unpurchased must not reuse the billing card")
}
