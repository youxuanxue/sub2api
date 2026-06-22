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

// newOAuth401AnthropicAccount 造一个 anthropic OAuth 账号，access_token 在 expiresAt 过期。
func newOAuth401AnthropicAccount(id int64, expiresAt time.Time) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"expires_at":    expiresAt.UTC().Format(time.RFC3339),
		},
	}
}

func TestRateLimitService_OAuth401_AnthropicAny401SetErrorsImmediately(t *testing.T) {
	cases := []struct {
		name      string
		expiresAt time.Time
	}{
		{"valid_token", time.Now().Add(2 * time.Hour)},
		{"expired_token", time.Now().Add(-1 * time.Hour)},
		{"near_expiry", time.Now().Add(1 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := newOAuth401AnthropicAccount(700, tc.expiresAt)

			shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

			require.True(t, shouldDisable)
			require.Equal(t, 1, repo.setErrorCalls, "anthropic OAuth 401 must SetError immediately")
			require.Equal(t, 0, repo.tempCalls, "must not temp_unschedulable / hold")
		})
	}
}

func TestRateLimitService_OAuth401_AnthropicMissingExpiryStillSetErrors(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       704,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
}

func TestRateLimitService_OAuth401_AnthropicMissingRefreshTokenSetErrors(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       707,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"expires_at": time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Contains(t, repo.lastErrorMsg, "OAuth 401")
}

func TestRateLimitService_OAuth401_AnthropicStillSetErrorsDuringClaudeIncident(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{IsIncident: true, Status: "major_outage", FetchedAt: time.Now()})
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := newOAuth401AnthropicAccount(708, time.Now().Add(-1*time.Hour))

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
}

func TestRateLimitService_OAuth401_SetupTokenStillSetErrorsDuringClaudeIncident(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{IsIncident: true, Status: "partial_outage", FetchedAt: time.Now()})
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       709,
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
		Credentials: map[string]any{
			"expires_at": time.Now().Add(300 * 24 * time.Hour).UTC().Format(time.RFC3339),
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
}

// OpenAI OAuth 仍保留「有效 token 永久禁用 / 过期 token 冷却」分支（非 Anthropic）。
func TestRateLimitService_OAuth401_OpenAIValidTokenDisablesFirstStrike(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       710,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
}

func TestRateLimitService_OAuth401_OpenAIExpiredTokenStillCools(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       711,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"expires_at":    time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
		},
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
}
