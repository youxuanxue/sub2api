//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRateLimitService_HandleUpstreamError_OpenAI403FirstHitTempUnschedulable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{1}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	account := &Account{
		ID:       301,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"temporary edge rejection"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "temporary edge rejection")
	require.Contains(t, repo.lastTempReason, "(1/3)")
}

func TestRateLimitService_HandleUpstreamError_OpenAI403ThresholdDisables(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &openAI403CounterCacheStub{counts: []int64{3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetOpenAI403CounterCache(counter)
	account := &Account{
		ID:       302,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"workspace forbidden by policy"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "workspace forbidden by policy")
	require.Contains(t, repo.lastErrorMsg, "consecutive_403=3/3")
}

func TestRateLimitService_HandleUpstreamError_Anthropic403ThresholdTempUnschedulable(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{1, 2, 3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{
		ID:       401,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	body := []byte(`{"type":"error","error":{"type":"permission_error","message":"OAuth token lacks required scopes"}}`)
	for i := 0; i < 2; i++ {
		shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)
		require.False(t, shouldDisable)
		require.Equal(t, 0, repo.tempCalls)
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)
	require.True(t, shouldDisable)

	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, []int64{401, 401, 401}, counter.incrementIDs)
	require.Equal(t, []int{anthropicUpstreamErrorWindowMinutes, anthropicUpstreamErrorWindowMinutes, anthropicUpstreamErrorWindowMinutes}, counter.windowMinutes)

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.lastTempReason), &state))
	require.Equal(t, http.StatusForbidden, state.StatusCode)
	require.Equal(t, "anthropic_upstream_error", state.MatchedKeyword)
	require.Contains(t, state.ErrorMessage, "OAuth token lacks required scopes")
}

// Anthropic apikey 池模式（上游是另一套 TokenKey / 兼容网关账号池）的账号必须
// 跳过 3/3 自动 temp_unschedulable：池前置代理自身会在内部轮换成员，偶发 5xx 是
// 池内调度抖动而非本账号故障，不应级联拉黑本地账号。
//
// 这条断言显式反转了 PR #248 (commit c62104ba) 的原设计 "pool-mode 仍计数"。
// 反转动因：prod cc-us1-oauth → edge-us1 转发链路下，cc-edges 单成员组被 10 分钟
// 自动暂禁会导致整个 group 0 可用账号、用户连续 503（2026-05-21 03:22 / 03:36
// 两次复现）。运维侧通过显式启用 credentials.pool_mode 表达"上游是池而非单点"，
// 接受失去 3/3 保护作为代价。
func TestRateLimitService_HandleUpstreamError_AnthropicPoolModeSkipsAutoUnsched(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{
		ID:       402,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode": true,
		},
	}

	for i := 0; i < 5; i++ {
		shouldDisable := service.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusBadGateway,
			http.Header{},
			[]byte(`{"error":{"message":"upstream edge failed"}}`),
		)
		require.False(t, shouldDisable, "iteration %d: pool_mode anthropic must not disable on 5xx", i)
	}

	require.Equal(t, 0, repo.setErrorCalls, "must not write account error state")
	require.Equal(t, 0, repo.tempCalls, "must not write temp_unschedulable")
	require.Empty(t, counter.incrementIDs, "must not even reach the counter increment")
}

// Carve-out: Anthropic accounts ignore the custom-error-codes allowlist so a
// non-listed 5xx still feeds the short-window counter. Without the
// `account.Platform != PlatformAnthropic` guard in HandleUpstreamError, an
// upstream merge that "simplifies" the early-return would silently drop the
// burst protection for any Anthropic APIKey customer who turned custom error
// codes on for, say, just 429.
func TestRateLimitService_HandleUpstreamError_AnthropicCustomErrorCodesStillCounts(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{counts: []int64{3}}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)
	account := &Account{
		ID:       403,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"custom_error_codes_enabled": true,
			"custom_error_codes":         []any{float64(429)},
		},
	}

	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusBadGateway,
		http.Header{},
		[]byte(`{"error":{"message":"upstream gateway timeout"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.tempCalls)
	require.Equal(t, []int64{403}, counter.incrementIDs)
}

// Recovery paths (ClearRateLimit / RecoverAccountAfterSuccessfulTest) must
// reset the Anthropic counter so a healed account does not carry stale strikes
// into the next short window. Mirrors the existing ResetOpenAI403Counter wiring.
func TestRateLimitService_ClearRateLimit_ResetsAnthropicCounter(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	require.NoError(t, service.ClearRateLimit(context.Background(), 404))
	require.Equal(t, []int64{404}, counter.resetCalls)
}

func TestRateLimitService_RecoverAccountAfterSuccessfulTest_ResetsAnthropicCounter(t *testing.T) {
	repo := &recoverableAccountRepoStub{
		rateLimitAccountRepoStub: rateLimitAccountRepoStub{},
		account: &Account{
			ID:       405,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusError,
		},
	}
	counter := &anthropicUpstreamErrorCounterCacheStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetAnthropicUpstreamErrorCounterCache(counter)

	result, err := service.RecoverAccountAfterSuccessfulTest(context.Background(), 405)
	require.NoError(t, err)
	require.True(t, result.ClearedError)
	require.Equal(t, []int64{405}, counter.resetCalls)
}

type recoverableAccountRepoStub struct {
	rateLimitAccountRepoStub
	account *Account
}

func (r *recoverableAccountRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.account, nil
}

func (r *recoverableAccountRepoStub) ClearError(ctx context.Context, id int64) error {
	r.account.Status = StatusActive
	return nil
}
