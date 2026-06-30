//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newEdgeUS4KiroAccount() *Account {
	return &Account{
		ID:       9,
		Name:     "kiro-us4-real",
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "at",
			"refresh_token": "rt",
			"profile_arn":   "arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		},
	}
}

func TestTkIsKiroQuotaLimit402_MatchesReachedTheLimit(t *testing.T) {
	account := newEdgeUS4KiroAccount()
	require.True(t, tkIsKiroQuotaLimit402(account, http.StatusPaymentRequired, "You have reached the limit.", nil))
	require.True(t, tkIsKiroQuotaLimit402(account, http.StatusPaymentRequired, "", []byte("You have reached the limit.")))
}

func TestTkIsKiroQuotaLimit402_NonKiroOrOther402NeverMatch(t *testing.T) {
	account := newEdgeUS4KiroAccount()
	for _, tc := range []struct {
		name       string
		account    *Account
		statusCode int
		msg        string
	}{
		{"openai 402", &Account{Platform: PlatformOpenAI}, http.StatusPaymentRequired, "You have reached the limit."},
		{"kiro 401", account, http.StatusUnauthorized, "You have reached the limit."},
		{"kiro 402 other message", account, http.StatusPaymentRequired, "Insufficient Balance"},
		{"nil account", nil, http.StatusPaymentRequired, "You have reached the limit."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, tkIsKiroQuotaLimit402(tc.account, tc.statusCode, tc.msg, nil))
		})
	}
}

func TestTkHandleKiroQuotaLimit402_DisablesAccountAndAlerts(t *testing.T) {
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	account := newEdgeUS4KiroAccount()

	handled := svc.tkHandleKiroQuotaLimit402(context.Background(), account, "You have reached the limit.", nil)
	require.True(t, handled)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "Payment required (402): You have reached the limit.")
	require.Len(t, blocker.reasons, 1)
	require.Equal(t, tkKiroQuotaLimitIncidentReason, blocker.reasons[0])
	require.Equal(t, []string{tkKiroQuotaLimitIncidentReason}, incidents.reasons)
	require.Contains(t, incidents.details[0], "402")
}

func TestHandleUpstreamError_Kiro402ReachedLimitUsesDedicatedReason(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	blocker := &runtimeBlockRecorder{}
	incidents := &tkBridgePenaltyIncidentRecorder{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAccountRuntimeBlocker(blocker)
	svc.SetAccountIncidentNotifier(incidents)

	account := newEdgeUS4KiroAccount()
	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusPaymentRequired, nil, []byte("You have reached the limit."))
	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, []string{tkKiroQuotaLimitIncidentReason}, incidents.reasons)
	require.NotContains(t, incidents.reasons, "auth_error")
}

func TestClassifyIncident_KiroQuotaLimit_IsImmediateP0(t *testing.T) {
	cls := classifyIncident(tkKiroQuotaLimitIncidentReason, time.Time{}, IncidentKindUnknown)
	require.True(t, cls.alert)
	require.Equal(t, IncidentKindPermanentDisable, cls.kind)
	require.Equal(t, "kiro_quota_limit", cls.reasonClass)
	require.Contains(t, cls.kindZh, "Kiro")
}
