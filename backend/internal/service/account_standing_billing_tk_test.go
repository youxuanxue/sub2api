//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const tokenseaQuota403Body = `{"error":{"type":"api_error","message":"用户额度不足, 剩余额度: ¥-0.065926 (request id: 202608301312116953332028268d9d6pZbBrFFG)"}}`

func TestTkIsAccountStandingBillingMessage_PositiveAndNegative(t *testing.T) {
	t.Parallel()
	positives := []string{
		"用户额度不足, 剩余额度: ¥-0.065926",
		"预扣费额度失败（剩 ¥3.88，需扣 ¥12.16）",
		"insufficient balance",
		"账户余额不足，请充值后重试",
		"your credit balance is too low",
		"is suspended due to insufficient balance, please recharge your account",
	}
	for _, msg := range positives {
		require.True(t, tkIsAccountStandingBillingMessage(strings.ToLower(msg)), msg)
		require.True(t, tkIsAccountStandingBillingFailure(msg, nil), msg)
	}

	negatives := []string{
		"You have exceeded the weekly usage quota. It will reset at 2026-08-30 23:59:59 +0800 CST",
		"Requests rate limit exceeded, please retry later",
		"insufficient_quota",
		"Invalid value for parameter 'temperature'",
		"do not have access to this model",
		"",
	}
	for _, msg := range negatives {
		require.False(t, tkIsAccountStandingBillingFailure(msg, nil), msg)
	}
}

func TestTkTryHandleStandingBilling_Tokensea403DisablesAndAlerts(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := &Account{
		ID:       93,
		Name:     "tokensea-cc",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
	}

	shouldDisable := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(tokenseaQuota403Body),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "prepaid 额度不足 must SetError, not stub-saturation failover")
	require.Zero(t, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "用户额度不足")
	require.Equal(t, []string{tkStandingBillingIncidentReason}, blocker.reasons)
	require.Equal(t, []string{tkStandingBillingIncidentReason}, incidents.reasons)
	require.Contains(t, incidents.details[0], "用户额度不足")
}

func TestTkTryHandleStandingBilling_SurvivesCanceledRequestContext(t *testing.T) {
	svc, repo, _, _ := newBridgePenaltyTestService()
	repo.failSetErrorOnCanceled = true
	account := &Account{
		ID:       93,
		Name:     "tokensea-cc",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	shouldDisable := svc.HandleUpstreamError(ctx, account, http.StatusForbidden, http.Header{}, []byte(tokenseaQuota403Body))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "SetError must survive canceled request ctx")
	require.Contains(t, repo.lastErrorMsg, "用户额度不足")
}

func TestTkTryHandleStandingBilling_WeeklyQuota429IsNotStanding(t *testing.T) {
	svc, repo, _, incidents := newBridgePenaltyTestService()
	account := &Account{
		ID:          88,
		Name:        "volcengine-agent-plan",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: 1,
	}
	body := []byte(`{"error":{"message":"You have exceeded the weekly usage quota. It will reset at 2026-08-30 23:59:59 +0800 CST"}}`)

	_ = svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)

	require.Zero(t, repo.setErrorCalls, "weekly usage window must not permanently disable")
	for _, reason := range incidents.reasons {
		require.NotEqual(t, tkStandingBillingIncidentReason, reason)
	}
}

func TestTkTryHandleStandingBilling_CNProviderSkipped(t *testing.T) {
	svc, repo, _, _ := newBridgePenaltyTestService()
	account := &Account{
		ID:       7,
		Name:     "kimi-cn",
		Platform: PlatformKimi,
		Type:     AccountTypeAPIKey,
	}

	handled := svc.tkTryHandleStandingBilling(
		context.Background(),
		account,
		http.StatusPaymentRequired,
		[]byte(`{"error":{"message":"余额不足"}}`),
	)
	require.False(t, handled, "CN providers keep the recoverable balance-probe loop")
	require.Zero(t, repo.setErrorCalls)
}

func TestClassifyIncident_StandingBillingUsesArrearsP0Card(t *testing.T) {
	cls := classifyIncident(tkStandingBillingIncidentReason, time.Time{}, IncidentKindUnknown)
	require.True(t, cls.alert)
	require.Equal(t, IncidentKindPermanentDisable, cls.kind)
	require.Equal(t, "newapi_arrears", cls.reasonClass)
	require.Equal(t, "上游账号欠费", cls.kindZh)
	require.Contains(t, cls.advice, "额度")
	require.Contains(t, cls.advice, "充值")
}
