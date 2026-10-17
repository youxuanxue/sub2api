//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTkIsDownstreamNoAvailableAccounts(t *testing.T) {
	require.True(t, tkIsDownstreamNoAvailableAccounts("", []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)))
	// Already-parsed upstream message.
	require.True(t, tkIsDownstreamNoAvailableAccounts("No available accounts: no available accounts", nil))
	// Case-insensitive.
	require.True(t, tkIsDownstreamNoAvailableAccounts("NO AVAILABLE ACCOUNTS", nil))
	// A genuine Anthropic upstream error must not match.
	require.False(t, tkIsDownstreamNoAvailableAccounts("Internal server error", []byte(`{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`)))
	require.False(t, tkIsDownstreamNoAvailableAccounts("", []byte(`{}`)))
}

func TestTkSkipDownstreamNoAvailableAccountsPenalty(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	require.True(t, tkSkipDownstreamNoAvailableAccountsPenalty(http.StatusServiceUnavailable, "", body))
	require.True(t, tkSkipDownstreamNoAvailableAccountsPenalty(http.StatusTooManyRequests, "", body))
	require.False(t, tkSkipDownstreamNoAvailableAccountsPenalty(http.StatusBadGateway, "", body))
	require.False(t, tkSkipDownstreamNoAvailableAccountsPenalty(http.StatusTooManyRequests, "", []byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`)))
}

func TestRateLimitService_HandleUpstreamError_DownstreamNoAvailable_DoesNotPenalizeStub(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 4042, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	for i := 0; i < 5; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body)
		require.True(t, shouldDisable, "iteration %d: downstream 503 no-available must fail over to the next stub", i)
	}

	require.Equal(t, 0, repo.tempCalls, "downstream no-available 503 must never write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "downstream no-available 503 must NOT advance the cooldown counter")
}

func TestRateLimitService_HandleUpstreamError_DownstreamNoAvailable429_DoesNotPenalizeStub(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 4042, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	headers := http.Header{}
	headers.Set("Retry-After", "5")
	for i := 0; i < 5; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, body)
		require.True(t, shouldDisable, "iteration %d: downstream 429 no-available must fail over to the next stub", i)
	}

	require.Equal(t, 0, repo.setRateLimitedCalls, "downstream no-available 429 must not write SetRateLimited")
	require.Equal(t, 0, repo.tempCalls, "downstream no-available 429 must never write temp_unschedulable")
	require.Equal(t, 0, repo.setErrorCalls)
	require.Empty(t, counter.incrementIDs, "downstream no-available 429 must NOT advance the cooldown counter")
}

// fakeSaturationCounterRL records IncrementSaturation calls for the increment-
// side test (read methods are unused on the rate-limit side).
type fakeSaturationCounterRL struct{ incrementIDs []int64 }

func (f *fakeSaturationCounterRL) IncrementSaturation(_ context.Context, accountID int64, _ int) (int64, error) {
	f.incrementIDs = append(f.incrementIDs, accountID)
	return int64(len(f.incrementIDs)), nil
}
func (f *fakeSaturationCounterRL) GetSaturationBatch(_ context.Context, _ []int64, _ int) (map[int64]int64, error) {
	return map[int64]int64{}, nil
}

// The saturation feature MUST feed the de-prioritization counter on the skip
// path while NEVER advancing the 3/3 ladder or writing temp_unschedulable /
// SetRateLimited. This is the load-bearing invariant: penalty, not cooldown.
func TestRateLimitService_DownstreamNoAvailable_FeedsSaturationButNotLadder(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	ladder := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3, 4, 5, 6}}
	sat := &fakeSaturationCounterRL{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(ladder)
	service.SetAnthropicSaturationCounter(sat)
	account := &Account{ID: 4044, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	// 429 empty-pool fast-fail.
	noAvail := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	for i := 0; i < 3; i++ {
		require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, noAvail))
	}
	// 502 failover-terminal (sibling downstream signal).
	exhausted := []byte(`{"type":"error","error":{"type":"server_error","message":"Upstream request could not be completed"}}`)
	for i := 0; i < 2; i++ {
		require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, exhausted))
	}

	require.Equal(t, []int64{4044, 4044, 4044, 4044, 4044}, sat.incrementIDs,
		"every downstream-capacity hit must feed the saturation preference counter")
	require.Empty(t, repo.modelRateLimitCalls, "mirror stub downstream-empty must not write model_rate_limits")
	require.Empty(t, ladder.incrementIDs, "saturation feature must NOT advance the 3/3 ladder")
	require.Equal(t, 0, repo.tempCalls, "saturation feature must NOT write temp_unschedulable")
	require.Equal(t, 0, repo.setRateLimitedCalls, "saturation feature must NOT write SetRateLimited")
	require.Equal(t, 0, repo.setErrorCalls)
}

// When the saturation counter is unwired (nil), the skip path behaves exactly as
// before — proving the increment is an optional, inert-by-default dependency.
func TestRateLimitService_DownstreamNoAvailable_NilSaturationCounterIsInert(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 4045, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	noAvail := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)
	require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, noAvail))
	require.Equal(t, 0, repo.tempCalls)
}

func TestRateLimitService_AnthropicMirrorSustainedEmptyPool_NoClassCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	sat := &fakeSaturationCounterRL{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicSaturationCounter(sat)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"type":"error","error":{"type":"api_error","message":"No available accounts: no available accounts"}}`)

	for i := 0; i < 4; i++ {
		require.True(t, service.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, "claude-sonnet-4-5"))
	}
	require.Equal(t, []int64{54, 54, 54, 54}, sat.incrementIDs)
	require.Empty(t, repo.modelRateLimitCalls)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Empty(t, repo.tempCalls)
}

func TestRateLimitService_AnthropicMirrorNonAuthoritative429_NoClassCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	sat := &fakeSaturationCounterRL{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicSaturationCounter(sat)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Upstream rate limit exceeded, please retry later"}}`)

	for i := 0; i < 4; i++ {
		require.True(t, service.HandleUpstreamError(context.Background(), account,
			http.StatusTooManyRequests, http.Header{}, body, "claude-sonnet-4-5"))
	}
	require.Equal(t, []int64{54, 54, 54, 54}, sat.incrementIDs)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestRateLimitService_AnthropicMirrorGenuine502_LegacyPath_NoClassCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	sat := &fakeSaturationCounterRL{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicSaturationCounter(sat)
	account := &Account{ID: 54, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"type":"error","error":{"type":"server_error","message":"Upstream request could not be completed"}}`)

	for i := 0; i < 5; i++ {
		require.True(t, service.HandleUpstreamError(context.Background(), account,
			http.StatusBadGateway, http.Header{}, body, "claude-sonnet-4-5"))
	}
	require.Empty(t, repo.modelRateLimitCalls)
	require.Equal(t, 0, repo.setRateLimitedCalls)
	require.Equal(t, 0, repo.tempCalls)
}

func TestRateLimitService_HandleUpstreamError_PlainAnthropicServiceUnavailable_StillCounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	ladder := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}, tierCounts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(ladder)
	account := &Account{ID: 67, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"Upstream service temporarily unavailable","type":"upstream_error"}}`)

	require.False(t, service.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, body))
	require.False(t, service.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, body))
	require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusBadGateway, http.Header{}, body))

	require.Equal(t, 1, repo.tempCalls, "real Anthropic 502 keeps the relay-health fuse")
	require.Equal(t, []int64{67, 67, 67}, ladder.incrementIDs)
}

func TestRateLimitService_HandleUpstreamError_PlainAnthropicAPIKey401_StillSetsError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 67, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"invalid x-api-key","type":"authentication_error"},"type":"error"}`)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "real Anthropic api-key 401 must keep the permanent-disable guard")
	require.Contains(t, repo.lastErrorMsg, "Authentication failed (401)")
}

func TestRateLimitService_HandleUpstreamError_PlainAnthropicAPIKey403_PermanentlyDisables(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{3}, tierCounts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 67, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	body := []byte(`{"error":{"message":"Invalid bearer token","type":"permission_error"},"type":"error"}`)

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Empty(t, counter.incrementIDs)
}

// Regression: a genuine Anthropic upstream 503 (no "no available accounts" body)
// still flows through the threshold path so persistent real upstream failure
// continues to escalate and eventually cools the account.
func TestRateLimitService_HandleUpstreamError_GenuineUpstream503_StillCounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}, tierCounts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{ID: 4043, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	body := []byte(`{"type":"error","error":{"type":"api_error","message":"upstream request failed"}}`)
	for i := 0; i < 2; i++ {
		require.False(t, service.HandleUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body))
	}
	require.True(t, service.HandleUpstreamError(context.Background(), account, http.StatusServiceUnavailable, http.Header{}, body),
		"3rd genuine upstream 503 must trip the threshold")
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, []int64{4043, 4043, 4043}, counter.incrementIDs)
}
