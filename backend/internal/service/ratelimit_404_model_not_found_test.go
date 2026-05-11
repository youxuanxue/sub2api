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
	lastAccountID       int64
	lastScope           string
	lastResetAt         time.Time
	setErrorCalls       int
	rateLimitCalls      int
}

func (r *anthropic404AccountRepoStub) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time) error {
	r.modelRateLimitCalls++
	r.lastAccountID = id
	r.lastScope = scope
	r.lastResetAt = resetAt
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

func TestHandleUpstreamError_Anthropic404ModelNotFoundSetsModelRateLimit(t *testing.T) {
	repo := &anthropic404AccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 42, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	body := []byte(`{"type":"error","error":{"type":"not_found_error","message":"model: claude-haiku-4-7"}}`)

	before := time.Now()
	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusNotFound, http.Header{}, body)
	after := time.Now()

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.modelRateLimitCalls)
	require.Equal(t, int64(42), repo.lastAccountID)
	require.Equal(t, "claude-haiku-4-7", repo.lastScope)
	require.True(t, !repo.lastResetAt.Before(before.Add(anthropic404ModelCooldown)) && !repo.lastResetAt.After(after.Add(anthropic404ModelCooldown)))
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
