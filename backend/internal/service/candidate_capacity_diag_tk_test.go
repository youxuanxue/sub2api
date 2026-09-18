//go:build unit

package service

import (
	"context"
	"testing"
	"time"

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
