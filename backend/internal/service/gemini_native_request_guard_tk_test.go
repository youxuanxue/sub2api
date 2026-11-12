//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type geminiCooldownStub struct {
	calls  int
	until  time.Time
	reason string
	err    error
}

func (s *geminiCooldownStub) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	s.calls++
	s.until = until
	s.reason = reason
	return s.err
}

func resetGeminiStreamDisruptionWindow() {
	geminiDisruptions.mu.Lock()
	geminiDisruptions.accounts = map[int64]*geminiDisruptionState{}
	geminiDisruptions.mu.Unlock()
}

func TestGeminiStreamDisruptionCoolsOnTheThirdEventInWindow(t *testing.T) {
	resetGeminiStreamDisruptionWindow()
	t.Cleanup(resetGeminiStreamDisruptionWindow)
	store := &geminiCooldownStub{}
	account := &Account{ID: 292}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

	noteGeminiStreamDisruptionAt(context.Background(), store, account, now)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(10*time.Minute))
	require.Zero(t, store.calls)

	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(14*time.Minute))
	require.Equal(t, 1, store.calls)
	require.Contains(t, store.reason, "gemini stream truncated or disconnected repeatedly")
	require.Contains(t, store.reason, "tier=0")
	require.Equal(t, now.Add(14*time.Minute).Add(anthropicCooldownTierLadder[0]), store.until)
}

func TestGeminiStreamDisruptionClimbsTheExistingCooldownLadder(t *testing.T) {
	resetGeminiStreamDisruptionWindow()
	t.Cleanup(resetGeminiStreamDisruptionWindow)
	store := &geminiCooldownStub{}
	account := &Account{ID: 292}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

	noteGeminiStreamDisruptionAt(context.Background(), store, account, now)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(time.Minute))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(2*time.Minute))
	require.Equal(t, 1, store.calls)
	require.Equal(t, now.Add(2*time.Minute).Add(anthropicCooldownTierLadder[0]), store.until)

	// Still inside the first cooldown: another full window must not climb.
	base := now.Add(2 * time.Minute)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, base.Add(5*time.Second))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, base.Add(10*time.Second))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, base.Add(15*time.Second))
	require.Equal(t, 1, store.calls)

	second := base.Add(anthropicCooldownTierLadder[0]).Add(time.Minute)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, second)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, second.Add(time.Minute))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, second.Add(2*time.Minute))
	require.Equal(t, 2, store.calls)
	require.Contains(t, store.reason, "tier=1")
	require.Equal(t, second.Add(2*time.Minute).Add(anthropicCooldownTierLadder[1]), store.until)
}

func TestGeminiStreamDisruptionLadderResetsAfterTheTierTTL(t *testing.T) {
	resetGeminiStreamDisruptionWindow()
	t.Cleanup(resetGeminiStreamDisruptionWindow)
	store := &geminiCooldownStub{}
	account := &Account{ID: 233}
	now := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(time.Minute))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(2*time.Minute))
	require.Equal(t, 1, store.calls)

	later := now.Add(time.Duration(anthropicCooldownTierTTLMinutes)*time.Minute + time.Minute)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, later)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, later.Add(time.Minute))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, later.Add(2*time.Minute))
	require.Equal(t, 2, store.calls)
	require.Contains(t, store.reason, "tier=0")
	require.Equal(t, later.Add(2*time.Minute).Add(anthropicCooldownTierLadder[0]), store.until)
}

func TestGeminiStreamDisruptionDropsEventsOutsideTheWindow(t *testing.T) {
	resetGeminiStreamDisruptionWindow()
	t.Cleanup(resetGeminiStreamDisruptionWindow)
	store := &geminiCooldownStub{}
	account := &Account{ID: 244}
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)

	noteGeminiStreamDisruptionAt(context.Background(), store, account, now)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(10*time.Minute))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(30*time.Minute))
	require.Zero(t, store.calls)
}

func TestGeminiG1CreditsCoolsImmediately(t *testing.T) {
	store := &geminiCooldownStub{}
	account := &Account{ID: 292}
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	noteGeminiG1CreditsExhaustedAt(context.Background(), store, account, now)
	require.Equal(t, 1, store.calls)
	require.Equal(t, "antigravity INSUFFICIENT_G1_CREDITS", store.reason)
	require.Equal(t, now.Add(geminiG1CreditsCooldown), store.until)
	require.True(t, antigravityG1CreditsExhausted([]byte(`{"error":{"details":[{"reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`)))
	require.False(t, antigravityG1CreditsExhausted([]byte(`{"error":{"message":"overloaded"}}`)))
}

func TestGeminiUpstreamUnexpectedEOFIgnoresClientCancel(t *testing.T) {
	require.True(t, geminiUpstreamUnexpectedEOF(io.ErrUnexpectedEOF))
	require.True(t, geminiUpstreamUnexpectedEOF(errors.New(`Post "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse": unexpected EOF`)))
	require.False(t, geminiUpstreamUnexpectedEOF(context.Canceled))
	require.False(t, geminiUpstreamUnexpectedEOF(nil))
}

func TestNativeGeminiEnvelopeRejectsForeignAndEmptyBodies(t *testing.T) {
	require.Equal(t, "This path only accepts a Gemini generateContent body",
		NativeGeminiGenerateContentEnvelopeError([]byte(`{"model":"gemini-4-argon","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`)))
	require.Equal(t, "This path only accepts a Gemini generateContent body",
		NativeGeminiGenerateContentEnvelopeError([]byte(`{"input":"hi","store":false}`)))
	require.Equal(t, "Gemini generateContent requires a non-empty contents array",
		NativeGeminiGenerateContentEnvelopeError([]byte(`{"generationConfig":{"maxOutputTokens":16}}`)))
	require.Equal(t, "Request body must be a JSON object",
		NativeGeminiGenerateContentEnvelopeError([]byte(`[]`)))
}

func TestNativeGeminiEnvelopeAllowsContentsAndNestedNames(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"},{"functionCall":{"name":"lookup","args":{"input":"paris","messages":[]}}}]}],"generationConfig":{"maxOutputTokens":16}}`)
	require.Empty(t, NativeGeminiGenerateContentEnvelopeError(body))
	require.False(t, GeminiBodyHasRemoteFileURI(body))
}

func TestAntigravityTransportsRejectRemoteFileURIBeforeUpstream(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/a.png"}}]}]}`)
	oauth := &Account{ID: 292, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	_, err := (*AntigravityGatewayService)(nil).ForwardGemini(context.Background(), nil, oauth, "gemini-4-argon", "generateContent", false, body, false)
	var media *AntigravityInlineMediaError
	require.ErrorAs(t, err, &media)

	apikey := &Account{ID: 244, Platform: PlatformAntigravity, Type: AccountTypeAPIKey}
	_, err = (*GeminiMessagesCompatService)(nil).ForwardNative(context.Background(), nil, apikey, "gemini-4-argon", "streamGenerateContent", true, body)
	require.ErrorAs(t, err, &media)

	official := &Account{ID: 7, Platform: PlatformGemini, Type: AccountTypeAPIKey}
	require.NoError(t, antigravityRemoteFileRejected(official, "generateContent", body))
	require.NoError(t, antigravityRemoteFileRejected(oauth, "countTokens", body))
}

func TestGeminiRemoteFileURIDetectsAntigravityUnsupportedReferences(t *testing.T) {
	inline := []byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`)
	require.False(t, GeminiBodyHasRemoteFileURI(inline))
	gs := []byte(`{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/a.png"}}]}]}`)
	require.True(t, GeminiBodyHasRemoteFileURI(gs))
	https := []byte(`{"contents":[{"parts":[{"file_data":{"file_uri":"https://example.test/a.png"}}]}]}`)
	require.True(t, GeminiBodyHasRemoteFileURI(https))
	require.Equal(t, "Antigravity Gemini only accepts inlineData; remote fileUri is not supported", AntigravityInlineMediaError{}.Error())
}
