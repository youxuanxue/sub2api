//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestCandidateCapacityDiagRejectSummaryStable(t *testing.T) {
	diag := &CandidateCapacityDiag{AccountTotal: 3, Supported: 2, Ready: 0}
	diag.reject("rpm_red")
	diag.reject("not_schedulable")
	diag.reject("rpm_red")
	require.Equal(t, "not_schedulable=1 rpm_red=2", diag.RejectSummary())
}

func TestCandidateCapacityDiagFromError(t *testing.T) {
	require.Nil(t, CandidateCapacityDiagFromError(ErrUniversalCapacityUnavailable))
	require.Nil(t, CandidateCapacityDiagFromError(nil))

	diag := &CandidateCapacityDiag{AccountTotal: 4, Supported: 1, Ready: 0}
	diag.reject("scheduling_threshold")
	err := newUniversalCapacityError(PlatformAnthropic, 1, diag)
	got := CandidateCapacityDiagFromError(err)
	require.NotNil(t, got)
	require.Equal(t, 4, got.AccountTotal)
	require.Equal(t, 1, got.Supported)
	require.Equal(t, 0, got.Ready)
	require.Equal(t, "scheduling_threshold=1", got.RejectSummary())
}

func TestGlobalCandidateCapacityErrorCarriesPathReadyDiag(t *testing.T) {
	groups := []Group{grp(10, PlatformOpenAI, 1, false)}
	accounts := []Account{globalCandidateAccount(1, 1, 10)}
	until := time.Now().Add(time.Hour)
	accounts[0].TempUnschedulableUntil = &until
	r, _, key := globalCandidateFixture(groups, accounts)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`)
	_, _, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions", "gpt-5.4", body, "", "")
	require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
	diag := CandidateCapacityDiagFromError(err)
	require.NotNil(t, diag, "capacity miss from candidate selection must carry diagnostics")
	require.Equal(t, 1, diag.AccountTotal)
	require.Equal(t, 1, diag.Supported)
	require.Equal(t, 0, diag.Ready)
	require.Contains(t, diag.RejectSummary(), "openai_not_schedulable=")
}

func TestGlobalCandidateCapacityErrorMarksSelectionExhausted(t *testing.T) {
	account := globalCandidateAccount(1, 1, 10)
	account.Platform = PlatformAnthropic
	account.Type = AccountTypeOAuth
	account.Extra = map[string]any{"max_sessions": 1}
	account.Credentials = map[string]any{
		"access_token":  "tok",
		"model_mapping": map[string]any{"claude-sonnet-4": "claude-sonnet-4"},
	}
	attachTestProtocolCapability(&account, protocolrouter.ProtocolMessages)
	groups := []Group{grp(10, PlatformAnthropic, 1, false)}
	r, _, key := globalCandidateFixture(groups, []Account{account})
	r.candidateGateway.sessionLimitCache = denySessionLimitCache{}
	body := []byte(`{"model":"claude-sonnet-4","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	ctx, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeAnthropicMessages, "/v1/messages", "claude-sonnet-4", body, "sess-deny", "")
	require.NoError(t, err)
	_, err = state.selectAccount(ctx, candidateSelectOptions{acquire: true})
	require.ErrorIs(t, err, ErrUniversalCapacityUnavailable)
	diag := CandidateCapacityDiagFromError(err)
	require.NotNil(t, diag)
	require.Equal(t, 1, diag.AccountTotal)
	require.Equal(t, 1, diag.Supported)
	require.Equal(t, 1, diag.Ready, "ready accounts entered the candidate pool before session registration failed")
	require.Contains(t, diag.RejectSummary(), "selection_exhausted=")
}

type denySessionLimitCache struct {
	SessionLimitCache
}

func (denySessionLimitCache) RegisterSession(context.Context, int64, string, int, time.Duration) (bool, error) {
	return false, nil
}
