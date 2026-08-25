//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const cloudwiseProvider424Body = `{"error":{"message":"Provider error (request id: aitk-test)","type":"server_error"}}`

func cloudwise424TestAccount(platform string) *Account {
	return &Account{
		ID:          94,
		Name:        "cloudwise",
		Platform:    platform,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://api.cloudwise.ai/api",
		},
	}
}

func cloudwise424ActiveModelCooldown(resetAt time.Time) map[string]any {
	return map[string]any{
		"rate_limited_at":     time.Now().UTC().Format(time.RFC3339),
		"rate_limit_reset_at": resetAt.UTC().Format(time.RFC3339),
		"reason":              "cloudwise_provider_error_424",
	}
}

func TestRateLimitService_CloudwiseProvider424_CoolsMappedModelForFifteenMinutes(t *testing.T) {
	account := cloudwise424TestAccount(PlatformAnthropic)
	account.Credentials["model_mapping"] = map[string]any{
		"claude-opus-latest": "claude-opus-4-8",
	}
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	startedAt := time.Now()
	shouldFailover := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusFailedDependency,
		http.Header{},
		[]byte(cloudwiseProvider424Body),
		"claude-opus-latest",
	)

	require.True(t, shouldFailover)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.tempCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, "claude-opus-4-8", call.scope)
	require.Equal(t, "cloudwise_provider_error_424", call.reason)
	require.WithinDuration(t, startedAt.Add(15*time.Minute), call.resetAt, time.Second)
}

func TestRateLimitService_CloudwiseProvider424_SecondDistinctModelEscalatesAccountForThirtyMinutes(t *testing.T) {
	now := time.Now()
	account := cloudwise424TestAccount(PlatformAnthropic)
	account.Extra = map[string]any{
		modelRateLimitsKey: map[string]any{
			"claude-opus-4-8": cloudwise424ActiveModelCooldown(now.Add(10 * time.Minute)),
		},
	}
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	startedAt := time.Now()
	shouldFailover := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusFailedDependency,
		http.Header{},
		[]byte(cloudwiseProvider424Body),
		"claude-sonnet-4-6",
	)

	require.True(t, shouldFailover)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "claude-sonnet-4-6", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, 1, repo.tempCalls)

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.lastTempReason), &state))
	require.Equal(t, http.StatusFailedDependency, state.StatusCode)
	require.Equal(t, "cloudwise_provider_error_424", state.MatchedKeyword)
	require.WithinDuration(t, startedAt.Add(30*time.Minute), time.Unix(state.UntilUnix, 0), time.Second)
}

func TestRateLimitService_CloudwiseProvider424_RepeatedSameModelDoesNotEscalateAccount(t *testing.T) {
	account := cloudwise424TestAccount(PlatformAnthropic)
	account.Extra = map[string]any{
		modelRateLimitsKey: map[string]any{
			"claude-opus-4-8": cloudwise424ActiveModelCooldown(time.Now().Add(10 * time.Minute)),
		},
	}
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	shouldFailover := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusFailedDependency,
		http.Header{},
		[]byte(cloudwiseProvider424Body),
		"claude-opus-4-8",
	)

	require.True(t, shouldFailover)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Zero(t, repo.tempCalls)
}

func TestRateLimitService_CloudwiseProvider424_WithoutModelUsesShortAccountCooldown(t *testing.T) {
	account := cloudwise424TestAccount(PlatformAnthropic)
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	startedAt := time.Now()
	shouldFailover := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusFailedDependency,
		http.Header{},
		[]byte(cloudwiseProvider424Body),
	)

	require.True(t, shouldFailover)
	require.Empty(t, repo.modelRateLimitCalls)
	require.Equal(t, 1, repo.tempCalls)

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.lastTempReason), &state))
	require.WithinDuration(t, startedAt.Add(30*time.Minute), time.Unix(state.UntilUnix, 0), time.Second)
}

func TestRateLimitService_CloudwiseProvider424_ModelCooldownWriteFailureUsesShortAccountCooldown(t *testing.T) {
	account := cloudwise424TestAccount(PlatformAnthropic)
	repo := &rateLimitAccountRepoStub{modelRateLimitErr: context.DeadlineExceeded}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	startedAt := time.Now()
	shouldFailover := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusFailedDependency,
		http.Header{},
		[]byte(cloudwiseProvider424Body),
		"claude-opus-4-8",
	)

	require.True(t, shouldFailover)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, 1, repo.tempCalls)

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.lastTempReason), &state))
	require.WithinDuration(t, startedAt.Add(30*time.Minute), time.Unix(state.UntilUnix, 0), time.Second)
}

func TestRateLimitService_NonCloudwiseOrNonProvider424_DoesNotCool(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		body    string
	}{
		{
			name: "official anthropic provider error",
			account: &Account{
				ID:       1,
				Platform: PlatformAnthropic,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"base_url": "https://api.anthropic.com",
				},
			},
			body: cloudwiseProvider424Body,
		},
		{
			name:    "cloudwise unrelated failed dependency",
			account: cloudwise424TestAccount(PlatformAnthropic),
			body:    `{"error":{"message":"request dependency missing","type":"invalid_request_error"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

			shouldDisable := svc.HandleUpstreamError(
				context.Background(),
				tt.account,
				http.StatusFailedDependency,
				http.Header{},
				[]byte(tt.body),
				"claude-opus-4-8",
			)

			require.False(t, shouldDisable)
			require.Empty(t, repo.modelRateLimitCalls)
			require.Zero(t, repo.tempCalls)
		})
	}
}

func TestOpenAIGateway_CloudwiseProvider424_ModelCooldownDoesNotRuntimeBlockWholeAccount(t *testing.T) {
	account := cloudwise424TestAccount(PlatformOpenAI)
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
	rateLimits.SetAccountRuntimeBlocker(gateway)

	shouldFailover := gateway.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusFailedDependency,
		http.Header{},
		[]byte(cloudwiseProvider424Body),
		"claude-opus-4-8",
	)

	require.True(t, shouldFailover)
	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
	require.Len(t, repo.modelRateLimitCalls, 1)
}
