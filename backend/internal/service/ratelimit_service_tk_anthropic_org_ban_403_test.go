//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// orgBan403AccountRepoStub captures SetError / SetTempUnschedulable so a test can
// assert which disable path handle403 took. Embedding the interface makes any
// unexpected method call panic.
type orgBan403AccountRepoStub struct {
	AccountRepository
	setErrorCalls    []setErrorCall
	tempUnschedCalls []tempUnschedCall
}

func (r *orgBan403AccountRepoStub) SetError(_ context.Context, id int64, errorMsg string) error {
	r.setErrorCalls = append(r.setErrorCalls, setErrorCall{accountID: id, reason: errorMsg})
	return nil
}

func (r *orgBan403AccountRepoStub) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.tempUnschedCalls = append(r.tempUnschedCalls, tempUnschedCall{accountID: id, until: until, reason: reason})
	return nil
}

// The canonical incident body (2026-06-16, edge us6 account edge-ls-oh-3-d).
const tkOrgBan403IncidentBody = `{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."}}`

func TestTryDisableAnthropicOrgBan403_MatchesOrgBan(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		body string
	}{
		{"oauth org ban via body", "", tkOrgBan403IncidentBody},
		{"oauth org ban via upstreamMsg", "OAuth authentication is currently not allowed for this organization.", ""},
		{"org disabled as 403 via body", "", `{"type":"error","error":{"type":"permission_error","message":"This organization has been disabled."}}`},
		{"mixed case", "", `{"error":{"message":"OAuth authentication is currently NOT ALLOWED FOR THIS ORGANIZATION."}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &orgBan403AccountRepoStub{}
			svc := &RateLimitService{accountRepo: repo}
			account := &Account{ID: 1, Name: "edge-ls-oh-3-d", Platform: PlatformAnthropic, Type: "oauth"}

			matched := svc.tkTryDisableAnthropicOrgBan403(context.Background(), account, tc.msg, []byte(tc.body))

			require.True(t, matched, "org-ban 403 must be recognized and escalated")
			require.Len(t, repo.setErrorCalls, 1, "must permanently disable via SetError")
			require.Empty(t, repo.tempUnschedCalls, "must NOT take the transient cooldown path")
			require.Equal(t, int64(1), repo.setErrorCalls[0].accountID)
			require.Contains(t, repo.setErrorCalls[0].reason, "Organization OAuth ban (403)")
		})
	}
}

func TestTryDisableAnthropicOrgBan403_IgnoresNonOrgBan403(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		body string
	}{
		{"generic permission error", "", `{"type":"error","error":{"type":"permission_error","message":"You do not have access to this model."}}`},
		// Boundary: a MODEL/feature-level "not allowed for this organization" denial
		// must NOT permanently disable a healthy account — only the account/auth-level
		// "authentication is currently not allowed for this organization" does. A
		// client requesting a model the org lacks must never poison the account.
		{"model-level not-allowed (must not disable)", "", `{"type":"error","error":{"type":"permission_error","message":"Model claude-opus-4 is not allowed for this organization."}}`},
		{"oauth token lacks scopes", "", `{"type":"error","error":{"type":"permission_error","message":"OAuth token lacks required scopes"}}`},
		{"tls/bot challenge", "", `<html>Just a moment... cloudflare challenge</html>`},
		{"empty body and msg", "", ""},
		{"unrelated organization word", "", `{"error":{"message":"Rate limit reached in organization org on tokens per min."}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &orgBan403AccountRepoStub{}
			svc := &RateLimitService{accountRepo: repo}
			account := &Account{ID: 2, Name: "acc-2", Platform: PlatformAnthropic}

			matched := svc.tkTryDisableAnthropicOrgBan403(context.Background(), account, tc.msg, []byte(tc.body))

			require.False(t, matched, "non-org-ban 403 must fall through to the existing cooldown path")
			require.Empty(t, repo.setErrorCalls)
			require.Empty(t, repo.tempUnschedCalls)
		})
	}
}

// handle403 end-to-end: the incident body must reach permanent disable (SetError)
// and report shouldDisable=true, NOT advance the transient 3/3 ladder.
func TestHandle403_AnthropicOrgBan_PermanentlyDisables(t *testing.T) {
	repo := &orgBan403AccountRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := &Account{ID: 1, Name: "edge-ls-oh-3-d", Platform: PlatformAnthropic, Type: "oauth"}

	shouldDisable := svc.handle403(
		context.Background(),
		account,
		"OAuth authentication is currently not allowed for this organization.",
		[]byte(tkOrgBan403IncidentBody),
	)

	require.True(t, shouldDisable, "org-ban 403 must report shouldDisable=true")
	require.Len(t, repo.setErrorCalls, 1, "must SetError (permanent disable, no auto-recovery)")
	require.Empty(t, repo.tempUnschedCalls, "must NOT cool down (which would auto-recover and re-offer the banned account)")
}
