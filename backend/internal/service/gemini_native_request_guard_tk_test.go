//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(20*time.Second))
	require.Zero(t, store.calls)

	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(40*time.Second))
	require.Equal(t, 1, store.calls)
	require.Contains(t, store.reason, "gemini stream truncated or disconnected repeatedly")
	require.Contains(t, store.reason, "tier=0")
	require.Equal(t, now.Add(40*time.Second).Add(anthropicCooldownTierLadder[0]), store.until)
}

func TestGeminiStreamDisruptionClimbsTheExistingCooldownLadder(t *testing.T) {
	resetGeminiStreamDisruptionWindow()
	t.Cleanup(resetGeminiStreamDisruptionWindow)
	store := &geminiCooldownStub{}
	account := &Account{ID: 292}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

	noteGeminiStreamDisruptionAt(context.Background(), store, account, now)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(20*time.Second))
	tripped := now.Add(40 * time.Second)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, tripped)
	require.Equal(t, 1, store.calls)
	require.Equal(t, tripped.Add(anthropicCooldownTierLadder[0]), store.until)

	// Still inside the first cooldown: another full window must not climb.
	noteGeminiStreamDisruptionAt(context.Background(), store, account, tripped.Add(5*time.Second))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, tripped.Add(10*time.Second))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, tripped.Add(15*time.Second))
	require.Equal(t, 1, store.calls)

	second := tripped.Add(anthropicCooldownTierLadder[0]).Add(time.Second)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, second)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, second.Add(20*time.Second))
	climbed := second.Add(40 * time.Second)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, climbed)
	require.Equal(t, 2, store.calls)
	require.Contains(t, store.reason, "tier=1")
	require.Equal(t, climbed.Add(anthropicCooldownTierLadder[1]), store.until)
}

func TestGeminiStreamDisruptionLadderResetsAfterTheTierTTL(t *testing.T) {
	resetGeminiStreamDisruptionWindow()
	t.Cleanup(resetGeminiStreamDisruptionWindow)
	store := &geminiCooldownStub{}
	account := &Account{ID: 233}
	now := time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(20*time.Second))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(40*time.Second))
	require.Equal(t, 1, store.calls)

	later := now.Add(time.Duration(anthropicCooldownTierTTLMinutes)*time.Minute + time.Minute)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, later)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, later.Add(20*time.Second))
	resetAt := later.Add(40 * time.Second)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, resetAt)
	require.Equal(t, 2, store.calls)
	require.Contains(t, store.reason, "tier=0")
	require.Equal(t, resetAt.Add(anthropicCooldownTierLadder[0]), store.until)
}

func TestGeminiStreamDisruptionDropsEventsOutsideTheWindow(t *testing.T) {
	resetGeminiStreamDisruptionWindow()
	t.Cleanup(resetGeminiStreamDisruptionWindow)
	store := &geminiCooldownStub{}
	account := &Account{ID: 244}
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)

	noteGeminiStreamDisruptionAt(context.Background(), store, account, now)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(40*time.Second))
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(70*time.Second))
	require.Zero(t, store.calls)
}

func TestGeminiStreamDisruptionIgnoresTruncationBeforeUsableOutput(t *testing.T) {
	resetGeminiStreamDisruptionWindow()
	t.Cleanup(resetGeminiStreamDisruptionWindow)
	store := &geminiCooldownStub{}
	account := &Account{ID: 529}
	now := time.Date(2026, 11, 13, 1, 30, 0, 0, time.UTC)

	for range 5 {
		noteGeminiStreamDisruptionWhenDelivered(context.Background(), store, account, false)
	}
	require.Zero(t, store.calls)

	noteGeminiStreamDisruptionAt(context.Background(), store, account, now)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(20*time.Second))
	require.Zero(t, store.calls)
	noteGeminiStreamDisruptionAt(context.Background(), store, account, now.Add(40*time.Second))
	require.Equal(t, 1, store.calls)
	require.Contains(t, store.reason, "tier=0")
}

func TestGeminiPayloadDeliveredUsableOutput(t *testing.T) {
	require.False(t, geminiPayloadDeliveredUsableOutput(nil))
	require.False(t, geminiPayloadDeliveredUsableOutput([]byte(`{"candidates":[{"content":{"parts":[{"thoughtSignature":"SIG"}]}}]}`)))
	require.False(t, geminiPayloadDeliveredUsableOutput([]byte(`{"candidates":[{"content":{"parts":[{"text":"  "}]}}]}`)))
	require.False(t, geminiPayloadDeliveredUsableOutput([]byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"hmm"}]}}]}`)))
	require.True(t, geminiPayloadDeliveredUsableOutput([]byte(`{"candidates":[{"content":{"parts":[{"text":"answer"}]}}]}`)))
	require.True(t, geminiPayloadDeliveredUsableOutput([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"x"}}]}}]}`)))
	require.True(t, geminiPayloadDeliveredUsableOutput([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"abc"}}]}}]}`)))
}

func TestGeminiPayloadStartsVisibleTTFT(t *testing.T) {
	require.False(t, geminiPayloadStartsVisibleTTFT(nil))
	require.False(t, geminiPayloadStartsVisibleTTFT([]byte(`{"candidates":[{"content":{"parts":[{"thoughtSignature":"SIG"}]}}]}`)))
	require.False(t, geminiPayloadStartsVisibleTTFT([]byte(`{"candidates":[{"content":{"parts":[{"text":"  "}]}}]}`)))
	require.False(t, geminiPayloadStartsVisibleTTFT([]byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"hmm"}]}}]}`)))
	require.False(t, geminiPayloadStartsVisibleTTFT([]byte(`{"usageMetadata":{"promptTokenCount":1}}`)))
	require.False(t, geminiPayloadStartsVisibleTTFT([]byte(`{"candidates":[{"finishReason":"STOP"}]}`)))
	require.True(t, geminiPayloadStartsVisibleTTFT([]byte(`{"candidates":[{"content":{"parts":[{"text":"answer"}]}}]}`)))
	require.True(t, geminiPayloadStartsVisibleTTFT([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"x"}}]}}]}`)))
	require.True(t, geminiPayloadStartsVisibleTTFT([]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"abc"}}]}}]}`)))
	// Mixed thought + visible text counts (visible part present).
	require.True(t, geminiPayloadStartsVisibleTTFT([]byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"hmm"},{"text":"hi"}]}}]}`)))
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
	out, err := antigravityPrepareRemoteMedia(context.Background(), official, "generateContent", body)
	require.NoError(t, err)
	require.Equal(t, body, out)
	out, err = antigravityPrepareRemoteMedia(context.Background(), oauth, "countTokens", body)
	require.NoError(t, err)
	require.Equal(t, body, out)
}

func TestAntigravityPrepareRemoteMediaInlinesHTTPSImages(t *testing.T) {
	prev := fetchAntigravityRemoteImage
	t.Cleanup(func() { fetchAntigravityRemoteImage = prev })
	fetchAntigravityRemoteImage = func(ctx context.Context, rawURL string) (antigravityFetchedImage, error) {
		require.Equal(t, "https://cdn.example.test/a.png", rawURL)
		return antigravityFetchedImage{Mime: "image/png", Data: []byte("hello")}, nil
	}

	body := []byte(`{"contents":[{"parts":[{"text":"see"},{"fileData":{"mimeType":"image/png","fileUri":"https://cdn.example.test/a.png"}}]}]}`)
	oauth := &Account{ID: 292, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	out, err := antigravityPrepareRemoteMedia(context.Background(), oauth, "generateContent", body)
	require.NoError(t, err)
	require.False(t, GeminiBodyHasRemoteFileURI(out))
	require.Equal(t, "image/png", gjson.GetBytes(out, "contents.0.parts.1.inlineData.mimeType").String())
	require.Equal(t, "aGVsbG8=", gjson.GetBytes(out, "contents.0.parts.1.inlineData.data").String())
	require.False(t, gjson.GetBytes(out, "contents.0.parts.1.fileData").Exists())
}

func TestAntigravityPrepareRemoteMediaRejectsPrivateImageHost(t *testing.T) {
	prev := fetchAntigravityRemoteImage
	t.Cleanup(func() { fetchAntigravityRemoteImage = prev })
	fetchAntigravityRemoteImage = defaultFetchAntigravityRemoteImage

	body := []byte(`{"contents":[{"parts":[{"file_data":{"mime_type":"image/*","file_uri":"http://127.0.0.1/a.png"}}]}]}`)
	oauth := &Account{ID: 292, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	_, err := antigravityPrepareRemoteMedia(context.Background(), oauth, "generateContent", body)
	var media *AntigravityInlineMediaError
	require.ErrorAs(t, err, &media)
	require.Contains(t, media.Error(), "invalid remote image URL")
}

func TestAntigravityRemoteImageURLAllowedBlocksPrivateHosts(t *testing.T) {
	require.Error(t, antigravityRemoteImageURLAllowed(context.Background(), "http://127.0.0.1/a.png"))
	require.Error(t, antigravityRemoteImageURLAllowed(context.Background(), "http://169.254.169.254/latest"))
	require.Error(t, antigravityRemoteImageURLAllowed(context.Background(), "gs://bucket/a.png"))
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
