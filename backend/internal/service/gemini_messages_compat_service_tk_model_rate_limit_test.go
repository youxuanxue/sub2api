//go:build unit

package service

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stubGeminiTKAccountRepo embeds AccountRepository so we can override only the
// limit-setting methods this TK feature exercises; everything else returns the
// embed's nil-pointer panic if accidentally invoked, which catches regressions
// where the wrong path is taken.
type stubGeminiTKAccountRepo struct {
	AccountRepository
	mu                  sync.Mutex
	rateCalls           []rateLimitCall
	modelRateLimitCalls []modelRateLimitCall
}

func (s *stubGeminiTKAccountRepo) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rateCalls = append(s.rateCalls, rateLimitCall{accountID: id, resetAt: resetAt})
	return nil
}

func (s *stubGeminiTKAccountRepo) SetModelRateLimit(ctx context.Context, id int64, modelKey string, resetAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelRateLimitCalls = append(s.modelRateLimitCalls, modelRateLimitCall{accountID: id, modelKey: modelKey, resetAt: resetAt})
	return nil
}

func newGeminiCodeAssistAccount(id int64) *Account {
	// project_id present + Type=oauth → IsGeminiCodeAssist() == true
	return &Account{
		ID:       id,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"project_id": "tk-test-project",
		},
	}
}

// TestExtractGeminiCodeAssistRateLimitedModel_ModelCapacityExhausted reproduces
// the 2026-05-06 prod payload: 429 RESOURCE_EXHAUSTED + MODEL_CAPACITY_EXHAUSTED
// with model in ErrorInfo.metadata.
func TestExtractGeminiCodeAssistRateLimitedModel_ModelCapacityExhausted(t *testing.T) {
	body := []byte(`{
		"error": {
			"code": 429,
			"message": "No capacity available for model gemini-3.1-pro-preview on the server",
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.ErrorInfo",
					"reason": "MODEL_CAPACITY_EXHAUSTED",
					"domain": "cloudcode-pa.googleapis.com",
					"metadata": {"model": "gemini-3.1-pro-preview"}
				}
			]
		}
	}`)

	require.Equal(t, "gemini-3.1-pro-preview", extractGeminiCodeAssistRateLimitedModel(body))
}

// TestExtractGeminiCodeAssistRateLimitedModel_NoModelMetadata covers the
// account-wide quota error (e.g. daily quota exhausted) where no per-model
// signal is present. We must return "" so the caller falls back to
// account-level rate limiting.
func TestExtractGeminiCodeAssistRateLimitedModel_NoModelMetadata(t *testing.T) {
	body := []byte(`{
		"error": {
			"code": 429,
			"message": "Quota exceeded",
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.QuotaFailure",
					"violations": [{"subject": "project:tk-test", "description": "daily quota"}]
				}
			]
		}
	}`)

	require.Equal(t, "", extractGeminiCodeAssistRateLimitedModel(body))
}

func TestExtractGeminiCodeAssistRateLimitedModel_MalformedBody(t *testing.T) {
	require.Equal(t, "", extractGeminiCodeAssistRateLimitedModel(nil))
	require.Equal(t, "", extractGeminiCodeAssistRateLimitedModel([]byte(`not-json`)))
	require.Equal(t, "", extractGeminiCodeAssistRateLimitedModel([]byte(`{}`)))
	require.Equal(t, "", extractGeminiCodeAssistRateLimitedModel([]byte(`{"error":{}}`)))
}

// TestTryGeminiCodeAssistApplyModelRateLimit_RecordsPerModel covers the prod
// behavior we want: a Code Assist 429 with model metadata writes ONLY a
// per-model rate limit, never an account-level one. Other models on the same
// account stay schedulable.
func TestTryGeminiCodeAssistApplyModelRateLimit_RecordsPerModel(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}
	account := newGeminiCodeAssistAccount(42)

	body := []byte(`{
		"error": {
			"code": 429,
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.ErrorInfo",
					"reason": "MODEL_CAPACITY_EXHAUSTED",
					"metadata": {"model": "gemini-3.1-pro-preview"}
				}
			]
		}
	}`)

	require.True(t, svc.tryGeminiCodeAssistApplyModelRateLimit(context.Background(), account, body, ""))
	require.Empty(t, repo.rateCalls, "must not set account-level rate limit when model is identified")
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, int64(42), repo.modelRateLimitCalls[0].accountID)
	require.Equal(t, "gemini-3.1-pro-preview", repo.modelRateLimitCalls[0].modelKey)
}

// TestTryGeminiCodeAssistApplyModelRateLimit_SkipsForNonCodeAssist ensures we
// do not change behavior for AI Studio OAuth or API Key accounts — those still
// take the upstream account-level path.
func TestTryGeminiCodeAssistApplyModelRateLimit_SkipsForNonCodeAssist(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}

	// AI Studio OAuth: no project_id, no oauth_type=code_assist
	aiStudio := &Account{
		ID:          7,
		Platform:    PlatformGemini,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"oauth_type": "ai_studio"},
	}
	body := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED","metadata":{"model":"gemini-3.1-pro-preview"}}]}}`)

	require.False(t, svc.tryGeminiCodeAssistApplyModelRateLimit(context.Background(), aiStudio, body, "gemini-3.1-pro-preview"))
	require.Empty(t, repo.modelRateLimitCalls, "AI Studio OAuth must not get per-model rate limit via this path")
	require.Empty(t, repo.rateCalls, "this helper never writes account-level — caller's fallback handles that")

	// API Key
	apiKey := &Account{
		ID:       8,
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey,
	}
	require.False(t, svc.tryGeminiCodeAssistApplyModelRateLimit(context.Background(), apiKey, body, "gemini-3.1-pro-preview"))
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestTryGeminiCodeAssistApplyModelRateLimit_FallsBackWhenNoModel covers the
// account-wide quota path: no model metadata → return false → caller does
// account-level fallback. This guarantees we don't silently swallow daily-quota
// 429s at model granularity.
func TestTryGeminiCodeAssistApplyModelRateLimit_FallsBackWhenNoModel(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}
	account := newGeminiCodeAssistAccount(11)

	body := []byte(`{
		"error": {
			"code": 429,
			"status": "RESOURCE_EXHAUSTED",
			"message": "Daily quota exceeded for project tk-test"
		}
	}`)

	require.False(t, svc.tryGeminiCodeAssistApplyModelRateLimit(context.Background(), account, body, ""))
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestTryGeminiCodeAssistApplyModelRateLimit_UsesFallbackModelForModelScoped429(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}
	account := newGeminiCodeAssistAccount(77)

	body := []byte(`{
		"error": {
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.ErrorInfo",
					"reason": "MODEL_CAPACITY_EXHAUSTED"
				}
			]
		}
	}`)

	require.True(t, svc.tryGeminiCodeAssistApplyModelRateLimit(context.Background(), account, body, "gemini-3.1-pro-preview"))
	require.Empty(t, repo.rateCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gemini-3.1-pro-preview", repo.modelRateLimitCalls[0].modelKey)
}

func TestTryGeminiCodeAssistApplyModelRateLimit_DoesNotUseFallbackForAccountWide429(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}
	account := newGeminiCodeAssistAccount(78)

	body := []byte(`{
		"error": {
			"code": 429,
			"status": "RESOURCE_EXHAUSTED",
			"message": "Daily quota exceeded for project tk-test"
		}
	}`)

	require.False(t, svc.tryGeminiCodeAssistApplyModelRateLimit(context.Background(), account, body, "gemini-3.1-pro-preview"))
	require.Empty(t, repo.modelRateLimitCalls)
}

// TestTryGeminiCodeAssistApplyModelRateLimit_RespectsParsedRetryDelay verifies
// that when the upstream provides quotaResetDelay / retryDelay, we honor it
// rather than always applying the tier cooldown — same precedence the
// account-level path uses.
func TestTryGeminiCodeAssistApplyModelRateLimit_RespectsParsedRetryDelay(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}
	account := newGeminiCodeAssistAccount(99)

	body := []byte(`{
		"error": {
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.ErrorInfo",
					"reason": "MODEL_CAPACITY_EXHAUSTED",
					"metadata": {"model": "gemini-3.1-pro-preview", "quotaResetDelay": "30s"}
				}
			]
		}
	}`)

	before := time.Now()
	require.True(t, svc.tryGeminiCodeAssistApplyModelRateLimit(context.Background(), account, body, ""))
	require.Len(t, repo.modelRateLimitCalls, 1)
	resetAt := repo.modelRateLimitCalls[0].resetAt
	// 30s ± clock drift (allow up to 5s slack).
	require.WithinDuration(t, before.Add(30*time.Second), resetAt, 5*time.Second,
		"upstream-provided quotaResetDelay must drive resetAt, not the tier cooldown")
}

// TestHandleGeminiUpstreamError_CodeAssist429RoutesToPerModel is the end-to-end
// check on the upstream call site we modified: a Code Assist 429 with model
// metadata must write per-model and skip the account-level write.
func TestHandleGeminiUpstreamError_CodeAssist429RoutesToPerModel(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}
	account := newGeminiCodeAssistAccount(123)

	body := []byte(`{
		"error": {
			"code": 429,
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.ErrorInfo",
					"reason": "MODEL_CAPACITY_EXHAUSTED",
					"domain": "cloudcode-pa.googleapis.com",
					"metadata": {"model": "gemini-3.1-pro-preview"}
				}
			]
		}
	}`)

	svc.handleGeminiUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body, "")

	require.Empty(t, repo.rateCalls, "Code Assist 429 with model metadata must NOT set account-level rate limit")
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gemini-3.1-pro-preview", repo.modelRateLimitCalls[0].modelKey)
}

func TestHandleGeminiUpstreamError_CodeAssist429FallbackModelRoutesToPerModel(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}
	account := newGeminiCodeAssistAccount(125)

	body := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`)

	svc.handleGeminiUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body, "gemini-3.1-pro-preview")

	require.Empty(t, repo.rateCalls, "model-scoped fallback must not set account-level rate limit")
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gemini-3.1-pro-preview", repo.modelRateLimitCalls[0].modelKey)
}

// TestHandleGeminiUpstreamError_CodeAssist429AccountWideStillFalls asserts the
// other half: when the body has no model metadata, we still fall back to the
// account-level rate limit (existing behavior unchanged).
func TestHandleGeminiUpstreamError_CodeAssist429AccountWideStillFalls(t *testing.T) {
	repo := &stubGeminiTKAccountRepo{}
	svc := &GeminiMessagesCompatService{accountRepo: repo}
	account := newGeminiCodeAssistAccount(124)

	body := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exceeded"}}`)

	svc.handleGeminiUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body, "")

	require.Empty(t, repo.modelRateLimitCalls, "no per-model signal → no per-model write")
	require.Len(t, repo.rateCalls, 1, "account-level fallback must still fire")
}
