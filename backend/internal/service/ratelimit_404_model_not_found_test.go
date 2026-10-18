//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type anthropic404AccountRepoStub struct {
	mockAccountRepoForGemini
	modelRateLimitCalls int
	setErrorCalls       int
	rateLimitCalls      int
}

func (r *anthropic404AccountRepoStub) SetModelRateLimit(_ context.Context, _ int64, _ string, _ time.Time, _ ...string) error {
	r.modelRateLimitCalls++
	return nil
}

func (r *anthropic404AccountRepoStub) SetError(_ context.Context, _ int64, _ string) error {
	r.setErrorCalls++
	return nil
}

func (r *anthropic404AccountRepoStub) SetRateLimited(_ context.Context, _ int64, _ time.Time) error {
	r.rateLimitCalls++
	return nil
}

func TestHandleUpstreamError_Anthropic404ModelNotFoundSkipsPenalty(t *testing.T) {
	repo := &anthropic404AccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 42, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	body := []byte(`{"type":"error","error":{"type":"not_found_error","message":"model: claude-haiku-4-7"}}`)

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusNotFound, http.Header{}, body)

	require.False(t, shouldDisable)
	require.Zero(t, repo.modelRateLimitCalls)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.rateLimitCalls)
}

func TestHandleUpstreamError_Anthropic404WithoutModelDoesNotDisableAccount(t *testing.T) {
	repo := &anthropic404AccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 43, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	body := []byte(`{"type":"error","error":{"type":"not_found_error","message":"resource not found"}}`)

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusNotFound, http.Header{}, body)

	require.False(t, shouldDisable)
	require.Zero(t, repo.modelRateLimitCalls)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.rateLimitCalls)
}

func TestHandleUpstreamError_NonAnthropic404DoesNotUseAnthropicModelProtection(t *testing.T) {
	repo := &anthropic404AccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 44, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"type":"not_found_error","message":"model: gpt-9"}}`)

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusNotFound, http.Header{}, body)

	require.False(t, shouldDisable)
	require.Zero(t, repo.modelRateLimitCalls)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.rateLimitCalls)
}

func TestHandleUpstreamError_Custom404DisablesTypeSafeOnly(t *testing.T) {
	opaque := []byte(`{"error":"not found"}`)

	t.Run("typesafe_custom_404_disables", func(t *testing.T) {
		repo := &anthropic404AccountRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		account := &Account{
			ID:       45,
			Platform: PlatformTypeSafe,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusNotFound)},
			},
		}

		shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusNotFound, http.Header{}, opaque)

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrorCalls)
		require.Zero(t, repo.modelRateLimitCalls)
	})

	t.Run("openai_custom_404_does_not_set_error", func(t *testing.T) {
		repo := &anthropic404AccountRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		account := &Account{
			ID:       46,
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusNotFound)},
			},
		}

		shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusNotFound, http.Header{}, opaque)

		require.False(t, shouldDisable)
		require.Zero(t, repo.setErrorCalls)
		require.Zero(t, repo.modelRateLimitCalls)
	})
}
