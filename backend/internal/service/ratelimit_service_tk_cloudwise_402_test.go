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

func TestRateLimitService_CloudwiseInsufficientBalance402_CoolsExactModelForFiveHours(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:          94,
		Name:        "cloudwise",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://api.cloudwise.ai/api",
		},
	}

	startedAt := time.Now()
	shouldFailover := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusPaymentRequired,
		http.Header{},
		[]byte(`{"error":{"message":"Insufficient balance","type":"insufficient_quota"}}`),
		"claude-opus-4-8",
	)

	require.True(t, shouldFailover, "current request must leave the exhausted account/model")
	require.Zero(t, repo.setErrorCalls, "CloudWise model balance must not disable the whole account")
	require.Len(t, repo.modelRateLimitCalls, 1)
	call := repo.modelRateLimitCalls[0]
	require.Equal(t, int64(94), call.accountID)
	require.Equal(t, "claude-opus-4-8", call.scope)
	require.Equal(t, "cloudwise_model_insufficient_balance_402", call.reason)
	require.WithinDuration(t, startedAt.Add(5*time.Hour), call.resetAt, time.Second)

	account.Extra = map[string]any{
		modelRateLimitsKey: map[string]any{
			call.scope: map[string]any{
				"rate_limit_reset_at": call.resetAt.Format(time.RFC3339),
				"reason":              call.reason,
			},
		},
	}
	require.False(t, account.IsSchedulableForModelWithContext(context.Background(), "claude-opus-4-8"))
	require.True(t, account.IsSchedulableForModelWithContext(context.Background(), "claude-sonnet-4-6"),
		"exact-model cooldown must leave sibling CloudWise models schedulable")
}

func TestOpenAIGateway_CloudwiseInsufficientBalance402_DoesNotRuntimeBlockWholeAccount(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	rateLimits := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	gateway := &OpenAIGatewayService{rateLimitService: rateLimits}
	rateLimits.SetAccountRuntimeBlocker(gateway)
	account := &Account{
		ID:          95,
		Name:        "cloudwise-openai",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://api.cloudwise.ai/api",
		},
	}
	body := []byte(`{"error":{"message":"Insufficient balance","type":"insufficient_quota"}}`)

	shouldFailover := gateway.handleOpenAIAccountUpstreamError(
		context.Background(),
		account,
		http.StatusPaymentRequired,
		http.Header{},
		body,
		"claude-opus-4-8",
	)

	require.True(t, shouldFailover)
	require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account),
		"CloudWise 402 must isolate the model without blocking sibling models on the account")
	require.Zero(t, repo.setErrorCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "claude-opus-4-8", repo.modelRateLimitCalls[0].scope)
}

func TestRateLimitService_CloudwisePoolMode402_StillCoolsExactModel(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:          96,
		Name:        "cloudwise-pool",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"base_url":  "https://api.cloudwise.ai/api",
			"pool_mode": true,
		},
	}

	shouldFailover := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusPaymentRequired,
		http.Header{},
		[]byte(`{"error":{"message":"Insufficient balance","type":"insufficient_quota"}}`),
		"claude-opus-4-8",
	)

	require.True(t, shouldFailover)
	require.Zero(t, repo.setErrorCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "claude-opus-4-8", repo.modelRateLimitCalls[0].scope)
}

func TestRateLimitService_Cloudwise402FallbackBypassesPoolAndCustomEarlyExits(t *testing.T) {
	tests := []struct {
		name        string
		credentials map[string]any
		model       []string
		writeErr    error
		wantWrites  int
	}{
		{
			name: "pool mode model cooldown write failure",
			credentials: map[string]any{
				"base_url":  "https://api.cloudwise.ai/api",
				"pool_mode": true,
			},
			model:      []string{"claude-opus-4-8"},
			writeErr:   errors.New("write failed"),
			wantWrites: 1,
		},
		{
			name: "custom error code miss after model cooldown write failure",
			credentials: map[string]any{
				"base_url":                   "https://api.cloudwise.ai/api",
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusTooManyRequests)},
			},
			model:      []string{"claude-opus-4-8"},
			writeErr:   errors.New("write failed"),
			wantWrites: 1,
		},
		{
			name: "pool mode without model context",
			credentials: map[string]any{
				"base_url":  "https://api.cloudwise.ai/api",
				"pool_mode": true,
			},
		},
		{
			name: "custom error code miss without model context",
			credentials: map[string]any{
				"base_url":                   "https://api.cloudwise.ai/api",
				"custom_error_codes_enabled": true,
				"custom_error_codes":         []any{float64(http.StatusTooManyRequests)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{modelRateLimitErr: tt.writeErr}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := &Account{
				ID:          100,
				Name:        "cloudwise-fallback",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Credentials: tt.credentials,
			}

			shouldDisable := svc.HandleUpstreamError(
				context.Background(),
				account,
				http.StatusPaymentRequired,
				http.Header{},
				[]byte(`{"error":{"message":"Insufficient balance","type":"insufficient_quota"}}`),
				tt.model...,
			)

			require.True(t, shouldDisable, "failed or impossible model cooldown must fail closed")
			require.Len(t, repo.modelRateLimitCalls, tt.wantWrites)
			require.Equal(t, 1, repo.setErrorCalls, "fallback must disable the whole account")
		})
	}
}

func TestRateLimitService_CloudwiseInsufficientBalance402_UsesMappedModelScope(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:          97,
		Name:        "cloudwise-mapped",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://api.cloudwise.ai/api",
			"model_mapping": map[string]any{
				"claude-opus-latest": "claude-opus-4-8",
			},
		},
	}

	shouldFailover := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusPaymentRequired,
		http.Header{},
		[]byte(`{"error":{"message":"Insufficient balance","type":"insufficient_quota"}}`),
		"claude-opus-latest",
	)

	require.True(t, shouldFailover)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "claude-opus-4-8", repo.modelRateLimitCalls[0].scope,
		"write key must match the mapped-model key consulted by scheduling")
}

func TestRateLimitService_Cloudwise402WithoutModel_KeepsAccountLevelFallback(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       98,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.cloudwise.ai/api",
		},
	}

	shouldDisable := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusPaymentRequired,
		http.Header{},
		[]byte(`{"error":{"message":"Insufficient balance","type":"insufficient_quota"}}`),
	)

	require.True(t, shouldDisable)
	require.Empty(t, repo.modelRateLimitCalls)
	require.Equal(t, 1, repo.setErrorCalls,
		"without a model key the safe fallback remains whole-account disable")
}

func TestRateLimitService_NonCloudwise402_KeepsAccountLevelBehavior(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       99,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.anthropic.com",
		},
	}

	shouldDisable := svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusPaymentRequired,
		http.Header{},
		[]byte(`{"error":{"message":"Insufficient balance","type":"insufficient_quota"}}`),
		"claude-opus-4-8",
	)

	require.True(t, shouldDisable)
	require.Empty(t, repo.modelRateLimitCalls)
	require.Equal(t, 1, repo.setErrorCalls)
}
