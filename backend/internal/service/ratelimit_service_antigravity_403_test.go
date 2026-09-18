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

func TestRateLimitService_HandleUpstreamError_AntigravityValidation403UsesTemporaryCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 701, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"status":"PERMISSION_DENIED","message":"VALIDATION_REQUIRED: Verify your account to continue.","details":[{"metadata":{"validation_url":"https://accounts.google.com/verify"}}]}}`)

	started := time.Now()
	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	require.True(t, shouldDisable, "the current request must fail over")
	require.Equal(t, 0, repo.setErrorCalls, "validation challenge must not permanently disable the credential")
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "Validation required (403)")
	require.Contains(t, repo.lastTempReason, "validation_url: https://accounts.google.com/verify")
	require.WithinDuration(t, started.Add(30*time.Minute), time.Now().Add(30*time.Minute), 2*time.Second)
}

func TestRateLimitService_HandleUpstreamError_AntigravityViolation403RemainsPermanent(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 702, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"message":"Terms of service violation"}}`)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "Account violation (403)")
}
