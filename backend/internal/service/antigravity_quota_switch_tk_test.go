//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAntigravityQuotaExhaustedSwitch(t *testing.T) {
	const quota = `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"158h"}}]}}`
	for _, tc := range []struct {
		name        string
		credits     bool
		preInjected bool
		wantCalls   int
	}{
		{"credits disabled", false, false, 1},
		{"credit attempt also failed", true, false, 2},
		{"credit already injected", true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &stubAntigravityAccountRepo{}
			cache := newTKStickyTestCache()
			svc := &AntigravityGatewayService{accountRepo: repo, cache: cache}
			account := &Account{ID: 103, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Extra: map[string]any{"allow_overages": tc.credits}}
			cache.entries["session"] = account.ID
			if tc.preInjected {
				account.Extra[modelRateLimitsKey] = map[string]any{
					"gemini-3.8-flash-high": map[string]any{"rate_limit_reset_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)},
				}
			}
			upstream := &queuedHTTPUpstreamStub{}
			for i := 0; i < tc.wantCalls; i++ {
				upstream.responses = append(upstream.responses, &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader(quota))})
			}
			params := newRetryParams(account, upstream, svc.handleUpstreamError)
			params.accountRepo = repo
			params.requestedModel = "gemini-3.8-flash"
			params.sessionHash = "session"
			before := time.Now()
			_, err := svc.antigravityRetryLoop(params)
			require.Equal(t, tc.wantCalls, upstream.callCount)
			if tc.credits {
				require.Contains(t, string(upstream.requestBodies[tc.wantCalls-1]), "enabledCreditTypes")
			}
			require.NotContains(t, cache.entries, "session")
			var switched *AntigravityAccountSwitchError
			require.ErrorAs(t, err, &switched)
			require.Equal(t, account.ID, switched.OriginalAccountID)
			require.NotEmpty(t, repo.modelRateLimitCalls)
			require.Equal(t, switched.RateLimitedModel, repo.modelRateLimitCalls[0].modelKey)
			require.WithinDuration(t, before.Add(antigravityMaxModelCooldown), repo.modelRateLimitCalls[0].resetAt, time.Second)
		})
	}
}
