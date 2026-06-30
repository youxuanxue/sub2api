//go:build unit

package service

import (
	"context"
	"errors"
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

func TestRateLimitService_OAuth401_KiroValidTokenForceRefreshesInsteadOfSetError(t *testing.T) {
	expiresAt := time.Now().Add(2 * time.Hour)
	account := newKiroOAuth401Account(720, expiresAt)
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuthRefreshAPI(NewOAuthRefreshAPI(repo, nil))
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "new-at",
			"refresh_token": "new-rt",
			"expires_at":    expiresAt.Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	service.SetKiroOAuthRefreshExecutor(executor)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("Invalid bearer token"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, executor.refreshCalls, "force refresh must ignore NeedsRefresh=false")
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, "new-at", repo.lastCredentials["access_token"])
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Equal(t, 1, repo.setSchedulableCalls)
	require.True(t, repo.lastSchedulable)
	require.Equal(t, 1, repo.clearTempCalls)
}

func TestRateLimitService_OAuth401_KiroValidTokenInvalidGrantFallsBackToSetError(t *testing.T) {
	account := newKiroOAuth401Account(721, time.Now().Add(2*time.Hour))
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuthRefreshAPI(NewOAuthRefreshAPI(repo, nil))
	service.SetKiroOAuthRefreshExecutor(&refreshAPIExecutorStub{
		needsRefresh: false,
		err:          errors.New("invalid_grant: token revoked"),
	})

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("Invalid bearer token"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "grant revoked upstream")
}

func TestRateLimitService_OAuth401_KiroValidTokenInvalidGrantRaceRecoveryClearsState(t *testing.T) {
	expiresAt := time.Now().Add(2 * time.Hour)
	account := newKiroOAuth401Account(723, expiresAt)
	recovered := newKiroOAuth401Account(723, expiresAt.Add(time.Hour))
	recovered.Credentials["refresh_token"] = "new-rt"
	repo := &rateLimitAccountRepoStub{
		getByIDAccounts: []*Account{
			account,   // OAuthRefreshAPI lock-protected reread before Refresh.
			recovered, // invalid_grant race recovery reread.
		},
	}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuthRefreshAPI(NewOAuthRefreshAPI(repo, nil))
	service.SetKiroOAuthRefreshExecutor(&refreshAPIExecutorStub{
		needsRefresh: false,
		err:          errors.New("invalid_grant: token already used"),
	})

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("Invalid bearer token"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Equal(t, 0, repo.updateCredentialsCalls)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Equal(t, 1, repo.setSchedulableCalls)
	require.True(t, repo.lastSchedulable)
	require.Equal(t, 1, repo.clearTempCalls)
}

func TestRateLimitService_OAuth401_KiroValidTokenTransientRefreshFailureCoolsDown(t *testing.T) {
	account := newKiroOAuth401Account(722, time.Now().Add(2*time.Hour))
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuthRefreshAPI(NewOAuthRefreshAPI(repo, nil))
	service.SetKiroOAuthRefreshExecutor(&refreshAPIExecutorStub{
		needsRefresh: false,
		err:          errors.New("network timeout"),
	})

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("Invalid bearer token"))

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "force-refresh failed")
}

func TestRateLimitService_OAuth403_KiroInvalidBearerForceRefreshesWithoutExpiry(t *testing.T) {
	account := newKiroOAuth401Account(724, time.Now().Add(2*time.Hour))
	delete(account.Credentials, "expires_at")
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuthRefreshAPI(NewOAuthRefreshAPI(repo, nil))
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials: map[string]any{
			"access_token":  "new-at",
			"refresh_token": "new-rt",
			"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	}
	service.SetKiroOAuthRefreshExecutor(executor)

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"message":"The bearer token included in the request is invalid.","reason":null}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, executor.refreshCalls, "invalid bearer 403 must force refresh even when expires_at is missing")
	require.Equal(t, 1, repo.updateCredentialsCalls)
	require.Equal(t, "new-at", repo.lastCredentials["access_token"])
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Equal(t, 1, repo.clearErrorCalls)
	require.Equal(t, 1, repo.setSchedulableCalls)
	require.True(t, repo.lastSchedulable)
	require.Equal(t, 1, repo.clearTempCalls)
}

func TestRateLimitService_OAuth403_KiroInvalidBearerInvalidGrantSetsError(t *testing.T) {
	account := newKiroOAuth401Account(725, time.Now().Add(2*time.Hour))
	delete(account.Credentials, "expires_at")
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuthRefreshAPI(NewOAuthRefreshAPI(repo, nil))
	service.SetKiroOAuthRefreshExecutor(&refreshAPIExecutorStub{
		needsRefresh: false,
		err:          errors.New("invalid_grant: token revoked"),
	})

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"message":"The bearer token included in the request is invalid.","reason":null}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "Access forbidden (403)")
	require.Contains(t, repo.lastErrorMsg, "bearer token")
}

func TestRateLimitService_OAuth403_KiroNonBearerStillSetErrors(t *testing.T) {
	account := newKiroOAuth401Account(726, time.Now().Add(2*time.Hour))
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOAuthRefreshAPI(NewOAuthRefreshAPI(repo, nil))
	executor := &refreshAPIExecutorStub{
		needsRefresh: false,
		credentials:  map[string]any{"access_token": "new-at"},
	}
	service.SetKiroOAuthRefreshExecutor(executor)

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"message":"policy denied"}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, executor.refreshCalls)
	require.Equal(t, 0, repo.updateCredentialsCalls)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
}

func newKiroOAuth401Account(id int64, expiresAt time.Time) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "old-at",
			"refresh_token": "old-rt",
			"expires_at":    expiresAt.UTC().Format(time.RFC3339),
			"auth_method":   "idc",
			"client_id":     "cid",
			"client_secret": "secret",
			"region":        "us-east-1",
			"profile_arn":   "arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		},
	}
}
