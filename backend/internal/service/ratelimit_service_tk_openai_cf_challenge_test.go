//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// TK regression for upstream Wei-Shaw/sub2api#1824 and #2413:
//
// OpenAI OAuth accounts hitting Cloudflare / Arkose challenges on
// /v1/images/{generations,edits} (and other automation-heavy paths) MUST
// NOT have their per-account 403 counter incremented and MUST NOT have a
// temp_unschedulable cooldown written. The OAuth identity is healthy; the
// 403 is per-request infrastructure noise. Cooldowns would poison the pool
// for unrelated traffic (chat completions, embeddings, non-image APIs).
//
// shouldDisable still returns true so the in-flight request fails over —
// this account cannot serve THIS request, but it can serve the next one.

func TestRateLimitService_HandleUpstreamError_OpenAI403CloudflareChallengeSkipsCooldown(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{
			name: "cloudflare_html_just_a_moment",
			body: []byte(`<!DOCTYPE html><html><head><title>Just a moment...</title></head><body><script>window._cf_chl_opt={};</script></body></html>`),
		},
		{
			name: "cloudflare_keyword_in_body",
			body: []byte(`Sorry, you have been blocked. This website is using a security service to protect itself from online attacks. Powered by Cloudflare.`),
		},
		{
			name: "arkose_funcaptcha_challenge",
			body: []byte(`<html><head></head><body><script src="https://client-api.arkoselabs.com/v2/funcaptcha/api.js"></script></body></html>`),
		},
		{
			name: "cf_challenge_platform_marker",
			body: []byte(`<html><body><script src="/cdn-cgi/challenge-platform/h/g/orchestrate/chl_page/v1"></script></body></html>`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			counter := &openAI403CounterCacheStub{counts: []int64{1}}
			service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			service.SetOpenAI403CounterCache(counter)

			account := &Account{
				ID:       901,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
			}

			shouldDisable := service.HandleUpstreamError(
				context.Background(),
				account,
				http.StatusForbidden,
				http.Header{"Cf-Ray": []string{"abc123-IAD"}},
				tc.body,
			)

			// In-flight request still fails over so the client retries on a
			// different OAuth account rather than receiving the CF page.
			require.True(t, shouldDisable, "shouldDisable must remain true so failover proceeds")

			// Critical invariant: no account-level penalty.
			require.Equal(t, 0, repo.tempCalls, "CF challenge must not write temp_unschedulable")
			require.Equal(t, 0, repo.setErrorCalls, "CF challenge must not SetError")
			require.Empty(t, counter.incrementIDs, "CF challenge must not increment the 403 counter")
		})
	}
}

// Regression guard: real OpenAI 403s (account suspended, lacks permissions,
// workspace deactivated etc.) still go through the normal counter +
// temp_unschedulable path. Without this guard, an over-eager keyword list
// or a future refactor could silently disable cooldowns for legitimate
// account-health 403s, masking real account problems.
func TestRateLimitService_HandleUpstreamError_OpenAI403RealAccountErrorStillCoolsDown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)

	account := &Account{
		ID:       902,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"You do not have permission to access this endpoint.","type":"forbidden","code":"account_disabled_auth_error"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.tempCalls, "real account 403 must still write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, []int64{902}, counter.incrementIDs, "real account 403 must still increment the counter")
	require.Contains(t, repo.lastTempReason, "You do not have permission to access this endpoint")
	require.Contains(t, repo.lastTempReason, "(1/3)")
}

// Regression guard: CF challenge keyword that appears INSIDE a structured
// OpenAI error body must still skip cooldown — this models the rare case
// where the upstream wraps a CF rejection inside its own error envelope.
// (Equally important: a regression that flips matching to JSON-aware would
// stop catching plain-HTML CF pages, which is the dominant in-prod shape.)
func TestRateLimitService_HandleUpstreamError_OpenAI403CFKeywordInsideStructuredBodyStillSkips(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)

	account := &Account{
		ID:       903,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"Request blocked by Cloudflare challenge","type":"forbidden"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.tempCalls)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs)
}

// Regression guard: non-OAuth (APIKey) accounts also benefit from the CF
// short-circuit — the keyword match runs before any OAuth-specific branch.
// CF challenges affect APIKey accounts proxied through CF-fronted endpoints
// just as much as OAuth accounts.
func TestRateLimitService_HandleUpstreamError_OpenAI403CFChallengeSkipsForAPIKey(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)

	account := &Account{
		ID:       904,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`<!DOCTYPE html><title>Just a moment...</title>`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.tempCalls)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs)
}
