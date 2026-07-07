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

func newOpenAIMirrorModelService(repo *rateLimitAccountRepoStub) (*RateLimitService, *fakeOpenAISaturationCounterRL) {
	sat := &fakeOpenAISaturationCounterRL{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetOpenAISaturationCounter(sat)
	return svc, sat
}

func TestOpenAIMirrorModel_SustainedSpark_WritesModelScopedCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newOpenAIMirrorModelService(repo)
	account := openAIEdgeStub(68)
	body := headerlessEmptyPoolBody()
	before := time.Now()

	for i := 0; i < 4; i++ {
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, codexSparkModel))
	}
	after := time.Now()

	require.NotEmpty(t, repo.modelRateLimitCalls)
	first := repo.modelRateLimitCalls[0]
	require.Equal(t, int64(68), first.accountID)
	require.Equal(t, codexSparkModel, first.scope)
	require.Equal(t, tkOpenAIMirrorDownstreamEmptyReason, first.reason)
	require.False(t, first.resetAt.Before(before))
	require.False(t, first.resetAt.After(after.Add(time.Duration(edgeMirrorStubSaturationWindowSeconds)*time.Second)))
	require.Zero(t, repo.setRateLimitedCalls)
	require.Empty(t, repo.tempCalls)
}

func TestOpenAIMirrorModel_BelowThreshold_NoCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newOpenAIMirrorModelService(repo)
	account := openAIEdgeStub(68)
	body := headerlessEmptyPoolBody()

	for i := 0; i < 2; i++ {
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, codexSparkModel))
	}
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestOpenAIMirrorModel_SparkCooled_GPT54StillSchedulable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc, _ := newOpenAIMirrorModelService(repo)
	account := openAIEdgeStub(68)
	body := headerlessEmptyPoolBody()

	for i := 0; i < 3; i++ {
		require.True(t, svc.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, codexSparkModel))
	}
	require.Len(t, repo.modelRateLimitCalls, 1)

	cooled := accountWithOpenAIModelCooldown(68, codexSparkModel, repo.modelRateLimitCalls[0].resetAt)
	ctx := context.Background()
	require.False(t, cooled.IsSchedulableForModelWithContext(ctx, codexSparkModel))
	require.True(t, cooled.IsSchedulableForModelWithContext(ctx, "gpt-5.4"))
}

func accountWithOpenAIModelCooldown(id int64, scope string, resetAt time.Time) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				scope: map[string]any{
					"rate_limit_reset_at": resetAt.Format(time.RFC3339),
				},
			},
		},
	}
}
