//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHandleGeminiUpstreamError_GoogleOneOAuth429UsesTierCooldownNotPSTMidnight
// pins the fix for upstream Wei-Shaw/sub2api#641:
//
// Gemini CLI google_one OAuth accounts that receive a 429 without an explicit
// reset header (no quotaResetDelay / retryDelay in the body) MUST use the tier
// cooldown (e.g. 5min for google_ai_pro) instead of falling through to the
// account-type "API Key / AI Studio" PST-midnight ban that could lock the
// account out for up to 24h.
func TestHandleGeminiUpstreamError_GoogleOneOAuth429UsesTierCooldownNotPSTMidnight(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}

	// google_one OAuth account on google_ai_pro tier (Cooldown: 5 * time.Minute
	// in newGeminiQuotaPolicy). project_id is empty, oauth_type explicitly
	// "google_one", so IsGeminiCodeAssist() returns false but Type=OAuth.
	account := &Account{
		ID:       641,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_type": "google_one",
			"tier_id":    GeminiTierGoogleAIPro,
		},
	}

	body := []byte(`{
		"error": {
			"code": 429,
			"status": "RESOURCE_EXHAUSTED",
			"message": "Rate limit exceeded"
		}
	}`)

	before := time.Now()
	svc.handleGeminiUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)

	require.Len(t, repo.rateCalls, 1, "google_one OAuth 429 must set account-level rate limit")
	resetAt := repo.rateCalls[0].resetAt

	cooldown := resetAt.Sub(before)
	require.Less(t, cooldown, 30*time.Minute, "google_one OAuth 429 cooldown must NOT be PST-midnight ban; must use tier cooldown (~5min for google_ai_pro)")
	require.Greater(t, cooldown, 30*time.Second, "google_one OAuth 429 cooldown must be a real tier cooldown, not zero")
}

// TestHandleGeminiUpstreamError_APIKey429StillFallsBackToPSTMidnight pins the
// other half of the fix: AI Studio API Key accounts (Type != OAuth) keep the
// PST-midnight fallback unchanged, since that matches Google's published daily
// quota reset behavior for the API Key path. We verify by computing the
// expected next-PST-midnight ourselves and asserting the resetAt matches it
// (within drift tolerance), rather than relying on a wall-clock difference
// that flakes near PST midnight.
func TestHandleGeminiUpstreamError_APIKey429StillFallsBackToPSTMidnight(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}

	account := &Account{
		ID:       6411,
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
	}

	body := []byte(`{
		"error": {
			"code": 429,
			"status": "RESOURCE_EXHAUSTED",
			"message": "Rate limit exceeded"
		}
	}`)

	expectedTs := nextGeminiDailyResetUnix()
	require.NotNil(t, expectedTs, "nextGeminiDailyResetUnix must be available in unit tests")
	expected := time.Unix(*expectedTs, 0)

	svc.handleGeminiUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)

	require.Len(t, repo.rateCalls, 1)
	resetAt := repo.rateCalls[0].resetAt
	require.WithinDuration(t, expected, resetAt, 5*time.Second,
		"API Key 429 must reset at next PST midnight, not via tier cooldown")
}
