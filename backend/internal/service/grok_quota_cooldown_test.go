//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrokQuotaQueryRemainsAvailableWhileSchedulingIsPaused(t *testing.T) {
	future := time.Now().Add(time.Hour)
	cases := []struct {
		name  string
		pause func(*Account)
	}{
		{"payment cooldown", func(a *Account) {
			a.TempUnschedulableUntil = &future
			a.TempUnschedulableReason = "grok payment required"
		}},
		{"rate limited", func(a *Account) { a.RateLimitResetAt = &future }},
		{"overloaded", func(a *Account) { a.OverloadUntil = &future }},
		{"manually paused", func(a *Account) { a.Schedulable = false }},
	}
	// Distinct account IDs per subtest, in a range no other test in this package
	// uses: grokObservedModelsFlight dedupes the background catalog sync by
	// account ID at package scope, so a shared ID would make each case's
	// upstream traffic depend on subtest order.
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := healthyGrokQuotaOAuthAccount(int64(1100 + i))
			tc.pause(account)
			repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
			provider := NewGrokTokenProvider(repo, nil)
			used := 100.0
			upstream := &grokHybridUpstream{weeklyUsagePercent: &used}
			svc := NewGrokQuotaService(repo, nil, provider, upstream, nil)

			result, err := svc.QueryQuota(context.Background(), account.ID)
			require.NoError(t, err)
			require.NotNil(t, result.Billing)
			require.Equal(t, used, *result.Billing.UsagePercent)
			// Assert per endpoint, not on the request count: QueryQuota also kicks
			// off scheduleGrokObservedModelsSync, whose background GET /v1/models
			// may or may not have landed by the time this line runs.
			requests, _ := upstream.quotaSnapshot()
			billing := 0
			for _, req := range requests {
				switch req.URL.Path {
				case "/v1/billing":
					billing++
					require.Equal(t, http.MethodGet, req.Method)
				case "/v1/models":
					// Read-only catalog sync, not model traffic.
					require.Equal(t, http.MethodGet, req.Method)
				default:
					// /v1/responses is the active quota probe: issuing it here would
					// spend quota on a paused account, which is what this test guards.
					t.Fatalf("unexpected upstream request while paused: %s %s", req.Method, req.URL.Path)
				}
			}
			require.Equal(t, 2, billing, "weekly + monthly billing fetches")
			// Observing quota must not resume model traffic or alter cooldowns.
			require.False(t, account.IsSchedulable())
			_, err = provider.GetAccessToken(context.Background(), account)
			require.ErrorIs(t, err, errOAuthRefreshAccountStateChanged)
			require.Zero(t, repo.tempUnschedCalls)
			require.Zero(t, repo.rateLimitedCalls)
			require.Zero(t, repo.recoveryClearCalls)
		})
	}
}

func TestGrokQuotaBillingStillRejectsUnavailableCredentialsDuringCooldown(t *testing.T) {
	cases := []struct {
		name       string
		invalidate func(*Account)
	}{
		{"missing refresh token", func(a *Account) { delete(a.Credentials, "refresh_token") }},
		{"expired access token without refresh service", func(a *Account) { a.Credentials["expires_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339) }},
		{"missing configured proxy", func(a *Account) { id := int64(42); a.ProxyID = &id }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := healthyGrokQuotaOAuthAccount(101)
			future := time.Now().Add(time.Hour)
			account.TempUnschedulableUntil = &future
			tc.invalidate(account)
			repo := &grokQuotaAccountRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{account.ID: account}}}
			upstream := &grokHybridUpstream{}
			svc := NewGrokQuotaService(repo, nil, NewGrokTokenProvider(repo, nil), upstream, nil)
			_, err := svc.ProbeBilling(context.Background(), account.ID)
			require.ErrorContains(t, err, "GROK_QUOTA_TOKEN_UNAVAILABLE")
			requests, _ := upstream.snapshot()
			require.Empty(t, requests)
		})
	}
}
