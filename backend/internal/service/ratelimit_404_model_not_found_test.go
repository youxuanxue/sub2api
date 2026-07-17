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

// TK (prod P0 2026-06-06, edge us5): an Anthropic 404 model-not-found is a
// CLIENT error (a model name no account can serve — Anthropic's catalog is
// global, not per-account), so it must NOT cool the account×model. Cooling a
// thin pool on a bad model name drained it into "No available accounts" 429s and
// amplified one misconfigured client into an edge-wide P0. This pins the
// no-penalty behavior (was: SetModelRateLimit + shouldDisable=true).
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
