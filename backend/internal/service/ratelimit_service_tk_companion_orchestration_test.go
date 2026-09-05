//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Pins the OpenAI 403 pre-penalty gate polarity that must stay identical to
// the pre-companion inline chain: CF / client-induced / openAIIsHTMLBody all
// return shouldDisable=true (failover without account penalty path); when
// none of those match, handled=false so the consecutive-403 counter runs.
func TestTkOpenAI403PrePenaltyGates_PolarityMatrix(t *testing.T) {
	svc := NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 4031, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	t.Run("cf_challenge_disables_request", func(t *testing.T) {
		body := []byte(`just a moment... challenge-platform cf-browser-verification`)
		handled, disable := svc.tkOpenAI403PrePenaltyGates(account, "", body)
		require.True(t, handled)
		require.True(t, disable)
	})

	t.Run("client_induced_capability_disables_request", func(t *testing.T) {
		msg := "You are not allowed to generate embeddings from this model"
		handled, disable := svc.tkOpenAI403PrePenaltyGates(account, msg, []byte(`{"error":{"message":"`+msg+`"}}`))
		require.True(t, handled)
		require.True(t, disable)
	})

	t.Run("openai_html_body_disables_request_not_account", func(t *testing.T) {
		handled, disable := svc.tkOpenAI403PrePenaltyGates(account, "", []byte(openAIAccessDeniedHTMLSample))
		require.True(t, handled)
		require.True(t, disable, "openAIIsHTMLBody branch must keep shouldDisable=true (skip cooldown, still failover)")
	})

	t.Run("structured_account_403_unhandled", func(t *testing.T) {
		body := []byte(`{"error":{"code":"access_terminated","message":"Your account has been disabled"}}`)
		handled, disable := svc.tkOpenAI403PrePenaltyGates(account, "Your account has been disabled", body)
		require.False(t, handled)
		require.False(t, disable)
	})

	// Dead-ish on typical HTML (openAIIsHTMLBody is broader), but the polarity
	// must stay: isHTMLResponse-only → shouldDisable=false (skip account
	// penalty AND do not force request failover via this gate).
	t.Run("is_html_response_only_skips_without_disable", func(t *testing.T) {
		body := []byte(`prefix <html><body>blocked</body></html>`)
		require.False(t, openAIIsHTMLBody(body))
		require.True(t, isHTMLResponse(body))
		handled, disable := svc.tkOpenAI403PrePenaltyGates(account, "", body)
		require.True(t, handled)
		require.False(t, disable)
	})

	t.Run("nil_account", func(t *testing.T) {
		handled, disable := svc.tkOpenAI403PrePenaltyGates(nil, "", nil)
		require.True(t, handled)
		require.False(t, disable)
	})
}

func TestTkTryHandle429Extras_RequestOwnedBeforeDownstream(t *testing.T) {
	svc := NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 4291, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	handled, disable := svc.tkTryHandle429Extras(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{},
		"Usage credits are required for long context requests.",
		nil,
	)
	require.True(t, handled)
	require.True(t, disable)

	handled, disable = svc.tkTryHandle429Extras(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{"Anthropic-Ratelimit-Unified-5h-Reset": {"9999999999"}},
		"Number of request tokens has exceeded your per-minute rate limit",
		[]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"rpm"}}`),
	)
	require.False(t, handled, "authoritative anthropic 429 must fall through to handle429")
	require.False(t, disable)
}

func TestTkHandleAuth401_CapabilityScopeDoesNotDisable(t *testing.T) {
	svc := NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 4011, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(tkCapabilityScope401IncidentBody)

	require.False(t, svc.tkHandleAuth401(context.Background(), account, "capability", body),
		"capability-scope 401 must not disable the account")
}

func TestTkHandleAuth401_NilGuards(t *testing.T) {
	var svc *RateLimitService
	require.False(t, svc.tkHandleAuth401(context.Background(), &Account{ID: 1}, "", nil))
	svc = NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
	require.False(t, svc.tkHandleAuth401(context.Background(), nil, "", nil))
}

func TestTkHandleNonOpenAI403_GenericPlatformDisables(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 55, Platform: PlatformGemini, Type: AccountTypeAPIKey}

	require.True(t, svc.tkHandleNonOpenAI403(context.Background(), account, "forbidden", []byte(`{"error":"no"}`)))
	require.Equal(t, 1, repo.setErrorCalls)
}
