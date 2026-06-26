//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// TK regression for prod P0 2026-06-25 (routing_capacity_rejection spike on
// gpt-5.5). A client sent `POST /v1/embeddings` with model=gpt-5.5; OpenAI
// replied 403 "You are not allowed to generate embeddings from this model".
// Before this fix, handleOpenAI403 treated that client-induced capability 403 as
// an account-level problem and wrote a 10-minute temp_unschedulable on the OAuth
// Codex accounts that actually serve gpt-5.5 chat traffic, emptying the pool and
// producing 628 "no available accounts" 429s until the cooldown lapsed.
//
// The 403 is deterministic per-request (every account 403s the same body), so it
// must fail over WITHOUT incrementing the counter or cooling the account.
func TestRateLimitService_HandleUpstreamError_OpenAI403EmbeddingsCapabilitySkipsCooldown(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{
			name: "embeddings_from_this_model",
			body: []byte(`{"error":{"message":"You are not allowed to generate embeddings from this model","type":"invalid_request_error","code":null}}`),
		},
		{
			name: "not_allowed_to_generate_embeddings",
			body: []byte(`{"error":{"message":"You are not allowed to generate embeddings.","type":"invalid_request_error"}}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			counter := &openAI403CounterCacheStub{counts: []int64{1}}
			service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			service.SetOpenAI403CounterCache(counter)

			account := &Account{
				ID:       73,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
			}

			shouldDisable := service.HandleUpstreamError(
				context.Background(),
				account,
				http.StatusForbidden,
				http.Header{},
				tc.body,
			)

			require.True(t, shouldDisable, "shouldDisable must stay true so the request fails over")
			require.Equal(t, 0, repo.tempCalls, "client-induced embeddings 403 must not write temp_unschedulable")
			require.Equal(t, 0, repo.setErrorCalls, "client-induced embeddings 403 must not SetError")
			require.Empty(t, counter.incrementIDs, "client-induced embeddings 403 must not increment the 403 counter")
		})
	}
}

// Guard against over-matching: a genuine account-level permission 403 (no
// embeddings-capability phrasing) must still go through the counter +
// temp_unschedulable path, otherwise real account problems would be masked.
func TestRateLimitService_HandleUpstreamError_OpenAI403GenericPermissionStillCoolsDown(t *testing.T) {
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
		[]byte(`{"error":{"message":"Your account is not allowed to access this resource.","type":"forbidden","code":"account_disabled"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.tempCalls, "real permission 403 must still write temp_unschedulable")
	require.Equal(t, []int64{902}, counter.incrementIDs)
}

// Predicate-level coverage, kept separate so a regression localises to the
// predicate vs. the handleOpenAI403 wiring.
func TestTkIsOpenAIClientInducedCapability403(t *testing.T) {
	match := []string{
		"You are not allowed to generate embeddings from this model",
		"not allowed to generate embeddings",
		`{"error":{"message":"You are not allowed to generate EMBEDDINGS from this model"}}`,
	}
	for _, m := range match {
		require.NotEmpty(t, tkIsOpenAIClientInducedCapability403(m, nil), "expected match for %q", m)
	}
	noMatch := []string{
		"",
		"Your account is suspended",
		"rate limit exceeded",
		"You do not have permission to access this endpoint",
	}
	for _, m := range noMatch {
		require.Empty(t, tkIsOpenAIClientInducedCapability403(m, nil), "expected no match for %q", m)
	}
}
